package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	App      AppConfig
	Database DatabaseConfig
	JWT      JWTConfig
	Server   ServerConfig
	OCR      OCRConfig
	Version  AppVersionConfig
}

type AppConfig struct {
	Name string
	Env  string
	Port string

	RegistrationOpen bool

	LogLevel string

	LogBodies bool
}

type DatabaseConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	DBName   string
	SSLMode  string
}

type JWTConfig struct {
	Secret           string
	ExpirationHours  int
	ExpirationTime   time.Duration
}

type OCRConfig struct {
	Bin         string        // OCR_TESSERACT_BIN (default "tesseract")
	Lang        string        // OCR_LANG (default "tha+eng")
	Timeout     time.Duration // OCR_TIMEOUT seconds per slip (default 30)
	Concurrency int           // OCR_CONCURRENCY reads at once (default 1)
	Dump bool
}

type AppVersionConfig struct {
	MinBuild    int    // MIN_APP_BUILD — older builds get 426 APP_OUTDATED (default 0 = off)
	LatestBuild int    // LATEST_APP_BUILD — newest build out there (default 0 = not set)
	DownloadURL string // APP_DOWNLOAD_URL — where testers get it (Firebase App Distribution)
	MessageTH   string // APP_UPDATE_MESSAGE_TH — optional text for the update screen
	MessageEN   string // APP_UPDATE_MESSAGE_EN
}

type ServerConfig struct {
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	config := &Config{
		App: AppConfig{
			Name:             getEnv("APP_NAME", "chubipocket"),
			Env:              getEnv("APP_ENV", "development"),
			Port:             getEnv("APP_PORT", "8080"),
			RegistrationOpen: getEnv("REGISTRATION_OPEN", "true") != "false",
			LogLevel:         getEnv("LOG_LEVEL", ""),
			LogBodies:        getEnv("LOG_BODIES", "false") == "true",
		},
		Database: DatabaseConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnv("DB_PORT", "5432"),
			User:     getEnv("DB_USER", "chubadmin"),
			Password: getEnv("DB_PASSWORD", "admin1234"),
			DBName:   getEnv("DB_NAME", "chubi_pocket_db"),
			SSLMode:  getEnv("DB_SSLMODE", "disable"),
		},
		JWT: JWTConfig{
			Secret:          getEnv("JWT_SECRET", "your-super-secret-jwt-key-change-this"),
			ExpirationHours: getEnvAsInt("JWT_EXPIRATION_HOURS", 24),
		},
		Server: ServerConfig{
			ReadTimeout:  time.Duration(getEnvAsInt("SERVER_READ_TIMEOUT", 10)) * time.Second,
			WriteTimeout: time.Duration(getEnvAsInt("SERVER_WRITE_TIMEOUT", 10)) * time.Second,
			IdleTimeout:  time.Duration(getEnvAsInt("SERVER_IDLE_TIMEOUT", 120)) * time.Second,
		},
		OCR: OCRConfig{
			Bin:         getEnv("OCR_TESSERACT_BIN", "tesseract"),
			Lang:        getEnv("OCR_LANG", "tha+eng"),
			Timeout:     time.Duration(getEnvAsInt("OCR_TIMEOUT", 30)) * time.Second,
			Concurrency: getEnvAsInt("OCR_CONCURRENCY", 1),
			Dump:        getEnv("OCR_DUMP", "") == "1",
		},
		Version: AppVersionConfig{
			MinBuild:    getEnvAsInt("MIN_APP_BUILD", 0),
			LatestBuild: getEnvAsInt("LATEST_APP_BUILD", 0),
			DownloadURL: getEnv("APP_DOWNLOAD_URL", ""),
			MessageTH:   getEnv("APP_UPDATE_MESSAGE_TH", ""),
			MessageEN:   getEnv("APP_UPDATE_MESSAGE_EN", ""),
		},
	}

	config.JWT.ExpirationTime = time.Duration(config.JWT.ExpirationHours) * time.Hour

	if err := config.Validate(); err != nil {
		return nil, err
	}

	return config, nil
}

func (c *Config) Validate() error {
	if c.Database.Host == "" {
		return fmt.Errorf("DB_HOST is required")
	}
	if c.Database.User == "" {
		return fmt.Errorf("DB_USER is required")
	}
	if c.Database.Password == "" {
		return fmt.Errorf("DB_PASSWORD is required")
	}
	if c.Database.DBName == "" {
		return fmt.Errorf("DB_NAME is required")
	}
	if c.JWT.Secret == "" || c.JWT.Secret == "your-super-secret-jwt-key-change-this" {
		if c.App.Env == "production" {
			return fmt.Errorf("JWT_SECRET must be changed in production")
		}
	}
	return nil
}

func (c *Config) GetDatabaseURL() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=%s",
		c.Database.User,
		c.Database.Password,
		c.Database.Host,
		c.Database.Port,
		c.Database.DBName,
		c.Database.SSLMode,
	)
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvAsInt(key string, defaultValue int) int {
	valueStr := getEnv(key, "")
	if value, err := strconv.Atoi(valueStr); err == nil {
		return value
	}
	return defaultValue
}

