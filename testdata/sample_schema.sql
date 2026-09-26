-- SchemaLens demo database: a small e-commerce shop.
--
-- It looks like a normal app schema, but it has a few mistakes planted on
-- purpose so every SchemaLens finding shows up in the demo:
--
--   missing_fk_index   order_items.product_id has an FK but no index
--   redundant_index    orders(user_id) is covered by orders(user_id, created_at)
--   duplicate_index    orders(status) is indexed twice
--   unused_index       order_items(unit_price_cents) is never used
--   fk_type_mismatch   billing.payments.order_id is integer, orders.id is bigint
--   inferred_relation  reviews.product_id points at products, but has no FK
--   no_primary_key     audit_log has no primary key
--
-- Also here to exercise the graph: a self-reference (categories.parent_id),
-- a 1:1 relation (user_profiles.user_id is unique), a many-to-many join
-- table with a composite key (product_categories), a cross-schema FK
-- (billing -> public) and a partitioned table (events, one partition a month).

SELECT setseed(0.42);  -- same "random" data on every load

CREATE SCHEMA billing;

-- ---------------------------------------------------------------------------
-- People
-- ---------------------------------------------------------------------------

CREATE TABLE users (
    id          bigserial PRIMARY KEY,
    email       text NOT NULL UNIQUE,
    full_name   text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE users IS 'Everyone who has signed up for the shop.';
COMMENT ON COLUMN users.email IS 'Login email, stored lower-case.';

CREATE TABLE user_profiles (
    id          bigserial PRIMARY KEY,
    user_id     bigint NOT NULL UNIQUE REFERENCES users (id) ON DELETE CASCADE,
    bio         text,
    avatar_url  text
);
COMMENT ON TABLE user_profiles IS 'Optional public profile, at most one per user.';

CREATE TABLE addresses (
    id          bigserial PRIMARY KEY,
    user_id     bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    line1       text NOT NULL,
    city        text NOT NULL,
    country     char(2) NOT NULL,
    is_default  boolean NOT NULL DEFAULT false
);
CREATE INDEX idx_addresses_user_id ON addresses (user_id);

-- ---------------------------------------------------------------------------
-- Catalogue
-- ---------------------------------------------------------------------------

CREATE TABLE categories (
    id         serial PRIMARY KEY,
    parent_id  integer REFERENCES categories (id),
    name       text NOT NULL
);
COMMENT ON TABLE categories IS 'Category tree. Top-level categories have no parent.';
CREATE INDEX idx_categories_parent_id ON categories (parent_id);

CREATE TABLE products (
    id           bigserial PRIMARY KEY,
    sku          text NOT NULL UNIQUE,
    name         text NOT NULL,
    price_cents  integer NOT NULL CHECK (price_cents >= 0),
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Many-to-many between products and categories.
CREATE TABLE product_categories (
    product_id   bigint NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    category_id  integer NOT NULL REFERENCES categories (id) ON DELETE CASCADE,
    PRIMARY KEY (product_id, category_id)
);
CREATE INDEX idx_product_categories_category_id ON product_categories (category_id);

-- Planted: product_id has no FOREIGN KEY constraint. SchemaLens should
-- guess the link to products from the column name.
CREATE TABLE reviews (
    id          bigserial PRIMARY KEY,
    product_id  bigint NOT NULL,
    user_id     bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    rating      smallint NOT NULL CHECK (rating BETWEEN 1 AND 5),
    body        text,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_reviews_product_id ON reviews (product_id);
CREATE INDEX idx_reviews_user_id ON reviews (user_id);

-- ---------------------------------------------------------------------------
-- Shopping
-- ---------------------------------------------------------------------------

CREATE TABLE coupons (
    id           serial PRIMARY KEY,
    code         text NOT NULL UNIQUE,
    percent_off  integer NOT NULL CHECK (percent_off BETWEEN 1 AND 100)
);

CREATE TABLE carts (
    id          bigserial PRIMARY KEY,
    user_id     bigint REFERENCES users (id) ON DELETE SET NULL,  -- NULL for guests
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_carts_user_id ON carts (user_id);

CREATE TABLE cart_items (
    cart_id     bigint NOT NULL REFERENCES carts (id) ON DELETE CASCADE,
    product_id  bigint NOT NULL REFERENCES products (id),
    quantity    integer NOT NULL DEFAULT 1,
    PRIMARY KEY (cart_id, product_id)
);
CREATE INDEX idx_cart_items_product_id ON cart_items (product_id);

CREATE TABLE orders (
    id           bigserial PRIMARY KEY,
    user_id      bigint NOT NULL REFERENCES users (id),
    coupon_id    integer REFERENCES coupons (id),
    status       text NOT NULL DEFAULT 'pending',
    total_cents  integer NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_orders_coupon_id ON orders (coupon_id);
CREATE INDEX idx_orders_user_id_created_at ON orders (user_id, created_at);
-- Planted: redundant, idx_orders_user_id_created_at already starts with user_id.
CREATE INDEX idx_orders_user_id ON orders (user_id);
-- Planted: the same index twice under two names.
CREATE INDEX idx_orders_status ON orders (status);
CREATE INDEX orders_status_idx ON orders (status);

CREATE TABLE order_items (
    id                bigserial PRIMARY KEY,
    order_id          bigint NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    -- Planted: FK with no index. Deleting a product scans all of order_items.
    product_id        bigint NOT NULL REFERENCES products (id),
    quantity          integer NOT NULL,
    unit_price_cents  integer NOT NULL
);
CREATE INDEX idx_order_items_order_id ON order_items (order_id);
-- Planted: nothing ever filters by price, so this index is never scanned.
CREATE INDEX idx_order_items_unit_price ON order_items (unit_price_cents);

CREATE TABLE shipments (
    id               bigserial PRIMARY KEY,
    order_id         bigint NOT NULL REFERENCES orders (id),
    carrier          text NOT NULL,
    tracking_number  text,
    shipped_at       timestamptz
);
CREATE INDEX idx_shipments_order_id ON shipments (order_id);

-- ---------------------------------------------------------------------------
-- Billing (separate schema, points back into public)
-- ---------------------------------------------------------------------------

CREATE TABLE billing.payments (
    id            bigserial PRIMARY KEY,
    -- Planted: integer, but orders.id is bigint.
    order_id      integer NOT NULL REFERENCES public.orders (id),
    amount_cents  integer NOT NULL,
    method        text NOT NULL,
    paid_at       timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE billing.payments IS 'One row per successful charge.';
CREATE INDEX idx_payments_order_id ON billing.payments (order_id);

CREATE TABLE billing.invoices (
    id          bigserial PRIMARY KEY,
    payment_id  bigint REFERENCES billing.payments (id),
    user_id     bigint NOT NULL REFERENCES public.users (id),
    number      text NOT NULL UNIQUE,
    issued_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_invoices_payment_id ON billing.invoices (payment_id);
CREATE INDEX idx_invoices_user_id ON billing.invoices (user_id);

-- ---------------------------------------------------------------------------
-- Logging
-- ---------------------------------------------------------------------------

-- Planted: no primary key.
CREATE TABLE audit_log (
    occurred_at  timestamptz NOT NULL DEFAULT now(),
    actor_id     bigint,
    action       text NOT NULL,
    payload      jsonb
);
COMMENT ON TABLE audit_log IS 'Append-only record of admin actions.';

-- Partitioned by month. SchemaLens should show this as one table.
CREATE TABLE events (
    id           bigserial,
    user_id      bigint REFERENCES users (id),
    kind         text NOT NULL,
    occurred_at  timestamptz NOT NULL,
    payload      jsonb,
    PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);
COMMENT ON TABLE events IS 'Product analytics events, one partition per month.';
CREATE INDEX idx_events_user_id ON events (user_id);

CREATE TABLE events_2026_01 PARTITION OF events FOR VALUES FROM ('2026-01-01') TO ('2026-02-01');
CREATE TABLE events_2026_02 PARTITION OF events FOR VALUES FROM ('2026-02-01') TO ('2026-03-01');
CREATE TABLE events_2026_03 PARTITION OF events FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE events_2026_04 PARTITION OF events FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE events_2026_05 PARTITION OF events FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE events_2026_06 PARTITION OF events FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

-- ---------------------------------------------------------------------------
-- Data. Enough rows that some tables pass the 10k "large table" mark.
-- ---------------------------------------------------------------------------

INSERT INTO users (email, full_name, created_at)
SELECT 'user' || i || '@example.com',
       'User ' || i,
       timestamptz '2025-01-01' + (i || ' minutes')::interval
FROM generate_series(1, 20000) AS i;

INSERT INTO user_profiles (user_id, bio)
SELECT i, 'Hi, I am user ' || i
FROM generate_series(1, 20000, 2) AS i;

INSERT INTO addresses (user_id, line1, city, country, is_default)
SELECT 1 + (i % 20000), i || ' Main Street', 'City ' || (i % 300),
       (ARRAY['US', 'GB', 'DE', 'IN', 'FR'])[1 + i % 5], i <= 20000
FROM generate_series(1, 25000) AS i;

INSERT INTO categories (parent_id, name)
SELECT NULL, 'Department ' || i FROM generate_series(1, 8) AS i;
INSERT INTO categories (parent_id, name)
SELECT 1 + (i % 8), 'Aisle ' || i FROM generate_series(1, 32) AS i;

INSERT INTO products (sku, name, price_cents)
SELECT 'SKU-' || lpad(i::text, 6, '0'), 'Product ' || i, 100 + (random() * 20000)::int
FROM generate_series(1, 5000) AS i;

INSERT INTO product_categories (product_id, category_id)
SELECT p, c
FROM generate_series(1, 5000) AS p,
     LATERAL (SELECT DISTINCT 9 + ((p * k) % 32) AS c FROM generate_series(1, 2) AS k) AS cats;

INSERT INTO reviews (product_id, user_id, rating, body)
SELECT 1 + (random() * 4999)::int, 1 + (random() * 19999)::int,
       1 + (random() * 4)::int, 'Review ' || i
FROM generate_series(1, 30000) AS i;

INSERT INTO coupons (code, percent_off)
SELECT 'SAVE' || i, 5 + (i % 30) FROM generate_series(1, 50) AS i;

INSERT INTO carts (user_id)
SELECT CASE WHEN i % 5 = 0 THEN NULL ELSE 1 + (random() * 19999)::int END
FROM generate_series(1, 8000) AS i;

INSERT INTO cart_items (cart_id, product_id, quantity)
SELECT c, 1 + ((c * 7 + k * 131) % 5000), 1 + k
FROM generate_series(1, 8000) AS c, generate_series(1, 2) AS k;

INSERT INTO orders (user_id, coupon_id, status, total_cents, created_at)
SELECT 1 + (random() * 19999)::int,
       CASE WHEN i % 10 = 0 THEN 1 + (i % 50) END,
       (ARRAY['pending', 'paid', 'shipped', 'delivered', 'cancelled'])[1 + i % 5],
       500 + (random() * 50000)::int,
       timestamptz '2025-06-01' + (i || ' minutes')::interval
FROM generate_series(1, 50000) AS i;

INSERT INTO order_items (order_id, product_id, quantity, unit_price_cents)
SELECT o, 1 + (random() * 4999)::int, 1 + (random() * 3)::int, 100 + (random() * 100000)::int
FROM generate_series(1, 50000) AS o, generate_series(1, 3) AS k;

INSERT INTO shipments (order_id, carrier, tracking_number, shipped_at)
SELECT i, (ARRAY['UPS', 'DHL', 'FedEx'])[1 + i % 3], 'TRK' || i,
       timestamptz '2025-06-02' + (i || ' minutes')::interval
FROM generate_series(1, 40000) AS i;

INSERT INTO billing.payments (order_id, amount_cents, method)
SELECT i, 500 + (random() * 50000)::int, (ARRAY['card', 'paypal', 'bank'])[1 + i % 3]
FROM generate_series(1, 45000) AS i;

INSERT INTO billing.invoices (payment_id, user_id, number)
SELECT i, 1 + (random() * 19999)::int, 'INV-' || lpad(i::text, 7, '0')
FROM generate_series(1, 40000) AS i;

INSERT INTO audit_log (actor_id, action, payload)
SELECT 1 + (i % 20), (ARRAY['login', 'refund', 'ban', 'edit_product'])[1 + i % 4],
       jsonb_build_object('n', i)
FROM generate_series(1, 10000) AS i;

INSERT INTO events (user_id, kind, occurred_at)
SELECT 1 + (random() * 19999)::int,
       (ARRAY['page_view', 'add_to_cart', 'checkout'])[1 + i % 3],
       timestamptz '2026-01-01' + ((i * 259) || ' seconds')::interval
FROM generate_series(1, 60000) AS i;

-- Give the planner real row counts, so reltuples is filled in. The tables are
-- listed so a non-superuser can run this file without catalog warnings.
ANALYZE users, user_profiles, addresses, categories, products, product_categories,
        reviews, coupons, carts, cart_items, orders, order_items, shipments,
        billing.payments, billing.invoices, audit_log, events;
