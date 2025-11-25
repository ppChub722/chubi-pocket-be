-- 1. Create Helper Function for Auto-Timestamp (Kept this from your code, it's good!)
CREATE OR REPLACE FUNCTION trigger_set_timestamp()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- 2. Create Users Table
CREATE TABLE users (
    -- CHANGE: Use BIGSERIAL for "running number" (1, 2, 3...) instead of UUID
    id BIGSERIAL PRIMARY KEY,
    
    -- CHANGE: Added username as requested
    username VARCHAR(50) UNIQUE NOT NULL,
    
    email VARCHAR(255) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    
    -- NOTE: I removed "full_name" because you didn't list it in your requirements. 
    -- If you want it back, just add: full_name VARCHAR(100),
    
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- 3. Create Indexes for Login Performance (Search by username OR email)
CREATE INDEX idx_users_username ON users(username);
CREATE INDEX idx_users_email ON users(email);

-- 4. Attach Timestamp Trigger
CREATE TRIGGER set_timestamp
BEFORE UPDATE ON users
FOR EACH ROW
EXECUTE PROCEDURE trigger_set_timestamp();