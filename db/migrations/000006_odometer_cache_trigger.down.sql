DROP TRIGGER IF EXISTS odometer_readings_refresh_cache_trigger ON odometer_readings;
DROP FUNCTION IF EXISTS odometer_readings_refresh_cache();
DROP FUNCTION IF EXISTS refresh_vehicle_mileage_cache(uuid);
