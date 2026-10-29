-- Roll back only the M2 idempotency records; the 0003 hold tables, every M1 row,
-- and the M0 platform metadata remain. Dropping the table removes its primary key
-- and the retention index.
DROP TABLE cinema.idempotency_keys;
DELETE FROM platform.schema_migrations WHERE version = '0004_idempotency';
