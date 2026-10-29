-- Roll back only M2 reservation objects; M0 platform metadata and M1 venue data remain.
-- Dropping reservation_seats removes the active-hold index and the claim trigger.
DROP TABLE cinema.reservation_seats;
DROP FUNCTION cinema.release_expired_seat_hold();
DROP TABLE cinema.reservations;
ALTER TABLE cinema.event_seats DROP CONSTRAINT event_seats_id_event_key;
DELETE FROM platform.schema_migrations WHERE version = '0003_reservation';
