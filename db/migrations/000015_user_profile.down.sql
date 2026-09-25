ALTER TABLE users
    DROP CONSTRAINT users_cnh_category_check,
    DROP CONSTRAINT users_phone_digits_check,
    DROP COLUMN photo_key,
    DROP COLUMN cnh_expires_on,
    DROP COLUMN cnh_category,
    DROP COLUMN phone,
    DROP COLUMN birth_date;
