package poc

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type JSON []byte

func (j JSON) Value() (driver.Value, error) {
	if len(j) == 0 {
		return nil, nil
	}
	return string(j), nil
}
func (j *JSON) Scan(v any) error {
	switch x := v.(type) {
	case []byte:
		*j = append((*j)[:0], x...)
	case string:
		*j = []byte(x)
	default:
		return fmt.Errorf("JSONB inválido: %T", v)
	}
	return nil
}
func (JSON) GormDataType() string { return "jsonb" }

type catalogVersion struct {
	Version    string `gorm:"primaryKey"`
	Snapshot   JSON
	Report     JSON
	ImportedAt time.Time `gorm:"default:now()"`
}

func (catalogVersion) TableName() string { return "catalog_versions" }

type catalogEntity struct {
	Version    string `gorm:"primaryKey"`
	Code       string `gorm:"primaryKey"`
	Kind       string
	ParentCode *string
	Data       JSON
}

func (catalogEntity) TableName() string { return "catalog_entities" }

type decision struct {
	IdempotencyKey string `gorm:"primaryKey"`
	DecisionID     string `gorm:"column:decision_id;type:uuid"`
	CatalogVersion string
	Result         JSON
	CreatedAt      time.Time `gorm:"default:now()"`
}

func (decision) TableName() string { return "decisions" }

type Result map[string]any

type Repository interface {
	Catalog(context.Context) (*Catalog, map[string]any, error)
	FindDecision(context.Context, string) (Result, error)
	SaveDecision(context.Context, string, Result) (Result, error)
}

type Store struct{ DB *gorm.DB }

func OpenStore(dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("falta DATABASE_URL")
	}
	if u, err := url.Parse(dsn); err == nil && (u.Scheme == "postgresql" || u.Scheme == "postgres") {
		q := u.Query()
		if q.Get("connect_timeout") == "" {
			q.Set("connect_timeout", "5")
		}
		u.RawQuery = q.Encode()
		dsn = u.String()
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, errors.New("no se pudo conectar a PostgreSQL")
	}
	pool, err := db.DB()
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(5)
	pool.SetMaxIdleConns(2)
	pool.SetConnMaxLifetime(30 * time.Minute)
	return &Store{DB: db}, nil
}
func (s *Store) Close() error {
	pool, err := s.DB.DB()
	if err != nil {
		return err
	}
	return pool.Close()
}
func (s *Store) Initialize(ctx context.Context) error {
	// Migración aditiva ejecutada a través de GORM. Respeta el esquema Python y sus FK.
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS catalog_versions(version TEXT PRIMARY KEY,snapshot JSONB NOT NULL,report JSONB NOT NULL,imported_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS catalog_entities(version TEXT NOT NULL REFERENCES catalog_versions(version),code TEXT NOT NULL,kind TEXT NOT NULL,parent_code TEXT,data JSONB NOT NULL,PRIMARY KEY(version,code),FOREIGN KEY(version,parent_code) REFERENCES catalog_entities(version,code))`,
		`CREATE TABLE IF NOT EXISTS decisions(idempotency_key TEXT PRIMARY KEY,decision_id UUID NOT NULL UNIQUE,catalog_version TEXT NOT NULL REFERENCES catalog_versions(version),result JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	} {
		if err := s.DB.WithContext(ctx).Exec(ddl).Error; err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) SaveCatalog(ctx context.Context, c *Catalog, report map[string]any) error {
	if c.Schema != CatalogSchema {
		return errors.New("esquema de catálogo no admitido; reimportar solicitudes estándar")
	}
	snapshot, err := json.Marshal(c)
	if err != nil {
		return err
	}
	rawReport, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := catalogVersion{Version: c.Version, Snapshot: snapshot, Report: rawReport}
		insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if insert.Error != nil {
			return insert.Error
		}
		if insert.RowsAffected == 0 {
			source, _ := json.Marshal(report["source_file"])
			return tx.Model(&catalogVersion{}).Where("version = ?", c.Version).UpdateColumn("report", gorm.Expr("jsonb_set(report, '{source_file}', ?::jsonb)", JSON(source))).Error
		}
		for _, kind := range GroupOrder {
			entities := []catalogEntity{}
			for _, entry := range c.Groups()[kind] {
				data, e := json.Marshal(entry)
				if e != nil {
					return e
				}
				var parent *string
				if p := str(entry, "parent_code"); p != "" {
					parent = &p
				}
				entities = append(entities, catalogEntity{Version: c.Version, Code: str(entry, "code"), Kind: kind, ParentCode: parent, Data: data})
			}
			if len(entities) > 0 {
				if err := tx.CreateInBatches(entities, 100).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}
func (s *Store) Catalog(ctx context.Context) (*Catalog, map[string]any, error) {
	var row catalogVersion
	err := s.DB.WithContext(ctx).Where("snapshot->>'schema' = ?", CatalogSchema).Order("imported_at DESC, version DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var c Catalog
	var report map[string]any
	if err = json.Unmarshal(row.Snapshot, &c); err != nil {
		return nil, nil, err
	}
	if err = json.Unmarshal(row.Report, &report); err != nil {
		return nil, nil, err
	}
	return &c, report, nil
}
func (s *Store) FindDecision(ctx context.Context, key string) (Result, error) {
	var row decision
	err := s.DB.WithContext(ctx).Where("idempotency_key = ?", key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result Result
	err = json.Unmarshal(row.Result, &result)
	return result, err
}
func (s *Store) SaveDecision(ctx context.Context, key string, result Result) (Result, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	row := decision{IdempotencyKey: key, DecisionID: result["decision_id"].(string), CatalogVersion: result["catalogo_version"].(string), Result: raw}
	if err = s.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "idempotency_key"}}, DoNothing: true}).Create(&row).Error; err != nil {
		return nil, err
	}
	return s.FindDecision(ctx, key)
}

func (s *Store) Verify(ctx context.Context) (map[string]any, error) {
	c, report, err := s.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errors.New("catálogo no disponible")
	}
	var before, after, count int64
	if err = s.DB.WithContext(ctx).Model(&catalogVersion{}).Count(&before).Error; err != nil {
		return nil, err
	}
	if err = s.SaveCatalog(ctx, c, report); err != nil {
		return nil, err
	}
	if err = s.DB.WithContext(ctx).Model(&catalogVersion{}).Count(&after).Error; err != nil {
		return nil, err
	}
	if before != after {
		return nil, errors.New("importación duplicada")
	}
	type groupCount struct {
		Kind  string
		Count int64
	}
	var groups []groupCount
	if err = s.DB.WithContext(ctx).Model(&catalogEntity{}).Select("kind, count(*) AS count").Where("version = ?", c.Version).Group("kind").Scan(&groups).Error; err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, g := range groups {
		counts[g.Kind] = g.Count
	}
	for kind, entries := range c.Groups() {
		if counts[kind] != int64(len(entries)) {
			return nil, fmt.Errorf("conteo inconsistente: %s", kind)
		}
	}
	if err = s.DB.WithContext(ctx).Model(&decision{}).Count(&count).Error; err != nil {
		return nil, err
	}
	var version string
	if err = s.DB.WithContext(ctx).Raw("SHOW server_version").Scan(&version).Error; err != nil {
		return nil, err
	}
	return map[string]any{"postgresql": version, "orm": "gorm", "catalog_versions": after, "entities": counts, "decisions": count, "duplicate_import": false}, nil
}
