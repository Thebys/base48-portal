-- Migration 005: Add projects table and update payments table
-- This adds support for fundraising/projects that can receive payments

-- Create projects table (simplified - no timestamps, no goal_amount, no is_active)
CREATE TABLE IF NOT EXISTS projects (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    payments_id TEXT UNIQUE,  -- VS projektu (např. "2024" pro energie)
    description TEXT
);

-- Add project_id column to payments table
ALTER TABLE payments ADD COLUMN project_id INTEGER REFERENCES projects(id);

-- Create index for faster project lookups
CREATE INDEX IF NOT EXISTS idx_payments_project ON payments(project_id);

-- Insert some example projects (optional - can be removed if not wanted)
INSERT OR IGNORE INTO projects (name, payments_id, description) VALUES
('Energie 2024', '2024', 'Nedoplatek za elektřinu a plyn za rok 2024');
