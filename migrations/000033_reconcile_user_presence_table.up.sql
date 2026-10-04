BEGIN;

-- user_presences is the only supported runtime table name. Do not merge two
-- tables automatically: simultaneous legacy and canonical tables require an
-- operator-led export and conflict review.
DO $$
BEGIN
    IF to_regclass('public.user_presence') IS NOT NULL
       AND to_regclass('public.user_presences') IS NOT NULL THEN
        RAISE EXCEPTION 'both user_presence and user_presences exist; refusing automatic merge';
    ELSIF to_regclass('public.user_presence') IS NOT NULL THEN
        ALTER TABLE user_presence RENAME TO user_presences;
    ELSIF to_regclass('public.user_presences') IS NULL THEN
        CREATE TABLE user_presences (
            id            VARCHAR(64) PRIMARY KEY,
            user_id       VARCHAR(64) NOT NULL,
            status        VARCHAR(16) DEFAULT 'offline',
            custom_status VARCHAR(128),
            last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
        );
    END IF;
END;
$$;

DO $$
DECLARE
    canonical_index_name TEXT := 'idx_user_presences_user_id';
    legacy_index_name TEXT;
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class AS index_class
        JOIN pg_index AS index_info ON index_info.indexrelid = index_class.oid
        JOIN pg_attribute AS column_info
          ON column_info.attrelid = index_info.indrelid
         AND column_info.attnum = index_info.indkey[0]
        WHERE index_class.oid = to_regclass('public.' || canonical_index_name)
          AND index_info.indrelid = 'public.user_presences'::regclass
          AND index_info.indisunique
          AND index_info.indnatts = 1
          AND column_info.attname = 'user_id'
    ) THEN
        NULL;
    ELSIF to_regclass('public.' || canonical_index_name) IS NOT NULL THEN
        RAISE EXCEPTION 'index public.% exists with an incompatible definition', canonical_index_name;
    ELSE
        SELECT indexrelid::regclass::text
        INTO legacy_index_name
        FROM pg_index
        JOIN pg_class AS index_class ON index_class.oid = indexrelid
        JOIN pg_attribute AS column_info
          ON column_info.attrelid = indrelid
         AND column_info.attnum = indkey[0]
        WHERE indrelid = 'public.user_presences'::regclass
          AND indisunique
          AND indnatts = 1
          AND column_info.attname = 'user_id'
        ORDER BY index_class.relname
        LIMIT 1;

        IF legacy_index_name IS NULL THEN
            CREATE UNIQUE INDEX idx_user_presences_user_id
                ON user_presences(user_id);
        END IF;
    END IF;
END;
$$;

DO $$
DECLARE
    legacy_constraint_name TEXT;
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_constraint
        JOIN pg_attribute AS child_column
          ON child_column.attrelid = pg_constraint.conrelid
         AND child_column.attnum = pg_constraint.conkey[1]
        JOIN pg_attribute AS parent_column
          ON parent_column.attrelid = pg_constraint.confrelid
         AND parent_column.attnum = pg_constraint.confkey[1]
        WHERE pg_constraint.conrelid = 'public.user_presences'::regclass
          AND pg_constraint.conname = 'fk_user_presences_user'
          AND pg_constraint.contype = 'f'
          AND pg_constraint.confrelid = 'public.users'::regclass
          AND pg_constraint.confdeltype = 'r'
          AND cardinality(pg_constraint.conkey) = 1
          AND cardinality(pg_constraint.confkey) = 1
          AND pg_constraint.conkey[1] = child_column.attnum
          AND pg_constraint.confkey[1] = parent_column.attnum
          AND child_column.attname = 'user_id'
          AND parent_column.attname = 'id'
    ) THEN
        NULL;
    ELSIF EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.user_presences'::regclass
          AND conname = 'fk_user_presences_user'
    ) THEN
        RAISE EXCEPTION 'constraint public.user_presences.fk_user_presences_user has an incompatible definition';
    ELSE
        SELECT child_constraint.conname
        INTO legacy_constraint_name
        FROM pg_constraint AS child_constraint
        JOIN pg_attribute AS child_column
          ON child_column.attrelid = child_constraint.conrelid
         AND child_column.attnum = child_constraint.conkey[1]
        JOIN pg_attribute AS parent_column
          ON parent_column.attrelid = child_constraint.confrelid
         AND parent_column.attnum = child_constraint.confkey[1]
        WHERE child_constraint.contype = 'f'
          AND child_constraint.conrelid = 'public.user_presences'::regclass
          AND child_constraint.confrelid = 'public.users'::regclass
          AND child_constraint.confdeltype = 'r'
          AND cardinality(child_constraint.conkey) = 1
          AND cardinality(child_constraint.confkey) = 1
          AND child_constraint.conkey[1] = child_column.attnum
          AND child_constraint.confkey[1] = parent_column.attnum
          AND child_column.attname = 'user_id'
          AND parent_column.attname = 'id'
        LIMIT 1;

        IF legacy_constraint_name IS NULL THEN
            ALTER TABLE user_presences
                ADD CONSTRAINT fk_user_presences_user
                FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT;
        END IF;
    END IF;
END;
$$;

COMMIT;
