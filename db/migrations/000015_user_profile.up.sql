-- Personal data on the account, all optional: birth date, phone, the driving licence's
-- category and expiry, and the object key of a profile photo in the bucket.
--
-- The phone is stored as digits only (DDD + number, 10 or 11 digits); the mask is the
-- client's. The licence category set is the CTB's (art. 143) plus the combined ones a
-- licence actually prints. photo_key names an object in private storage; the URL a client
-- sees is signed per response and never stored.

ALTER TABLE users
    ADD COLUMN birth_date     date,
    ADD COLUMN phone          text,
    ADD COLUMN cnh_category   text,
    ADD COLUMN cnh_expires_on date,
    ADD COLUMN photo_key      text,
    ADD CONSTRAINT users_phone_digits_check
        CHECK (phone IS NULL OR phone ~ '^[0-9]{10,11}$'),
    ADD CONSTRAINT users_cnh_category_check
        CHECK (cnh_category IS NULL OR cnh_category IN (
            'A', 'B', 'AB', 'C', 'D', 'E', 'AC', 'AD', 'AE'
        ));
