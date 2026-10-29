-- Roll back only M1 objects; M0 platform metadata remains.
DROP SCHEMA cinema CASCADE;
DELETE FROM platform.schema_migrations WHERE version = '0002_venue_layout';
