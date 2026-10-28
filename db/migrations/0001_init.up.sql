-- M0 foundation: platform metadata only. Domain tables begin in M1.
CREATE SCHEMA platform;

CREATE TABLE platform.schema_migrations (
  version text PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE platform.settings (
  key text PRIMARY KEY,
  value jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO platform.schema_migrations (version) VALUES ('0001_init');
