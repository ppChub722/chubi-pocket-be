# 🚀 FinaBBear Deployment Guide

Complete guide for deploying the FinaBBear API to production environments.

---

## 📚 Table of Contents

1. [Pre-Deployment Checklist](#pre-deployment-checklist)
2. [Environment Configuration](#environment-configuration)
3. [Database Setup](#database-setup)
4. [Deployment Methods](#deployment-methods)
   - [Docker Deployment](#docker-deployment)
   - [Traditional Server Deployment](#traditional-server-deployment)
   - [Cloud Platform Deployment](#cloud-platform-deployment)
5. [Security Hardening](#security-hardening)
6. [Monitoring & Logging](#monitoring--logging)
7. [Backup & Recovery](#backup--recovery)
8. [CI/CD Pipeline](#cicd-pipeline)
9. [Troubleshooting](#troubleshooting)

---

## ✅ Pre-Deployment Checklist

Before deploying to production, ensure:

### Code Readiness
- [ ] All tests passing: `go test ./...`
- [ ] No linter errors: `go vet ./...`
- [ ] Code reviewed and approved
- [ ] Dependencies up to date: `go mod tidy`
- [ ] Security vulnerabilities checked

### Configuration
- [ ] Production `.env` file prepared
- [ ] Strong JWT secret generated
- [ ] Database credentials secured
- [ ] CORS settings configured
- [ ] Rate limiting configured

### Database
- [ ] Production database created
- [ ] Migrations tested and ready
- [ ] Database backups configured
- [ ] Connection pooling optimized

### Infrastructure
- [ ] Server/Cloud resources provisioned
- [ ] Domain name configured
- [ ] SSL/TLS certificates ready
- [ ] Firewall rules set up
- [ ] Monitoring tools configured

---

## ⚙️ Environment Configuration

### Production `.env` File

Create a production-specific `.env` file:

```env
# Application
APP_NAME=FinaBBear
APP_ENV=production
APP_PORT=8080

# Database (Use production credentials)
DB_HOST=your-db-host.com
DB_PORT=5432
DB_USER=finabbear_prod
DB_PASSWORD=STRONG_RANDOM_PASSWORD_HERE
DB_NAME=finabbear_production
DB_SSLMODE=require

# JWT Authentication (Generate strong secret!)
JWT_SECRET=GENERATE_STRONG_RANDOM_SECRET_HERE
JWT_EXPIRATION_HOURS=24

# Server Configuration
SERVER_READ_TIMEOUT=15
SERVER_WRITE_TIMEOUT=15
SERVER_IDLE_TIMEOUT=180

# Logging
LOG_LEVEL=info

# Optional: External Services
# REDIS_URL=redis://your-redis-host:6379
# SMTP_HOST=smtp.gmail.com
# SMTP_PORT=587
```

### Generate Strong Secrets

**JWT Secret (Linux/Mac):**
```bash
openssl rand -base64 32
```

**JWT Secret (Windows PowerShell):**
```powershell
[Convert]::ToBase64String((1..32 | ForEach-Object { Get-Random -Minimum 0 -Maximum 256 }))
```

**Or use online generator:**
- https://generate-secret.vercel.app/32

### Environment Variables Best Practices

1. **Never commit production `.env` to git**
2. **Use different secrets for each environment**
3. **Rotate secrets regularly**
4. **Store secrets in secure vault (HashiCorp Vault, AWS Secrets Manager)**
5. **Minimum password length: 32 characters**

---

## 🗄️ Database Setup

### Production Database Configuration

#### 1. Create Production Database

```sql
-- Connect as superuser
CREATE DATABASE finabbear_production;

-- Create dedicated user with strong password
CREATE USER finabbear_prod WITH PASSWORD 'STRONG_PASSWORD_HERE';

-- Grant necessary privileges
GRANT ALL PRIVILEGES ON DATABASE finabbear_production TO finabbear_prod;

-- Connect to the database
\c finabbear_production

-- Grant schema privileges
GRANT ALL ON SCHEMA public TO finabbear_prod;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO finabbear_prod;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO finabbear_prod;

-- Default privileges for future objects
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO finabbear_prod;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO finabbear_prod;
```

#### 2. Run Production Migrations

```bash
# Set production database URL
export DB_URL="postgres://finabbear_prod:PASSWORD@your-db-host.com:5432/finabbear_production?sslmode=require"

# Run migrations
migrate -path migrations -database "$DB_URL" up

# Verify
migrate -path migrations -database "$DB_URL" version
```

#### 3. Database Connection Pooling

Configure in your application:
```env
DB_MAX_CONNECTIONS=25
DB_MIN_CONNECTIONS=5
DB_MAX_IDLE_TIME=300
```

#### 4. Enable SSL/TLS

```env
DB_SSLMODE=require
# Or for stricter security:
DB_SSLMODE=verify-full
DB_SSLCERT=/path/to/client-cert.pem
DB_SSLKEY=/path/to/client-key.pem
DB_SSLROOTCERT=/path/to/ca-cert.pem
```

---

## 🐳 Docker Deployment

### Method 1: Docker Compose (Recommended for small deployments)

#### 1. Create Dockerfile

**File:** `Dockerfile`
```dockerfile
# Build stage
FROM golang:1.25.3-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main ./cmd/api

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /root/

# Copy binary from builder
COPY --from=builder /app/main .
COPY --from=builder /app/migrations ./migrations

# Expose port
EXPOSE 8080

# Run the application
CMD ["./main"]
```

#### 2. Create .dockerignore

**File:** `.dockerignore`
```
.env
.git
.gitignore
.vscode
.idea
*.md
bin/
dist/
logfile
*.log
```

#### 3. Create docker-compose.yml

**File:** `docker-compose.yml`
```yaml
version: '3.8'

services:
  # PostgreSQL Database
  postgres:
    image: postgres:18-alpine
    container_name: finabbear_db
    environment:
      POSTGRES_DB: finabbear_production
      POSTGRES_USER: finabbear_prod
      POSTGRES_PASSWORD: ${DB_PASSWORD}
    volumes:
      - postgres_data:/var/lib/postgresql/data
    ports:
      - "5432:5432"
    networks:
      - finabbear_network
    restart: unless-stopped
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U finabbear_prod"]
      interval: 10s
      timeout: 5s
      retries: 5

  # FinaBBear API
  api:
    build:
      context: .
      dockerfile: Dockerfile
    container_name: finabbear_api
    environment:
      APP_NAME: FinaBBear
      APP_ENV: production
      APP_PORT: 8080
      DB_HOST: postgres
      DB_PORT: 5432
      DB_USER: finabbear_prod
      DB_PASSWORD: ${DB_PASSWORD}
      DB_NAME: finabbear_production
      DB_SSLMODE: disable
      JWT_SECRET: ${JWT_SECRET}
      JWT_EXPIRATION_HOURS: 24
    ports:
      - "8080:8080"
    depends_on:
      postgres:
        condition: service_healthy
    networks:
      - finabbear_network
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "--quiet", "--tries=1", "--spider", "http://localhost:8080/ping"]
      interval: 30s
      timeout: 10s
      retries: 3

  # Nginx Reverse Proxy (Optional)
  nginx:
    image: nginx:alpine
    container_name: finabbear_nginx
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
      - ./ssl:/etc/nginx/ssl:ro
    ports:
      - "80:80"
      - "443:443"
    depends_on:
      - api
    networks:
      - finabbear_network
    restart: unless-stopped

volumes:
  postgres_data:
    driver: local

networks:
  finabbear_network:
    driver: bridge
```

#### 4. Create .env for Docker Compose

**File:** `.env.production`
```env
DB_PASSWORD=your_strong_db_password
JWT_SECRET=your_strong_jwt_secret
```

#### 5. Deploy with Docker Compose

```bash
# Build images
docker-compose build

# Start services
docker-compose up -d

# Check status
docker-compose ps

# View logs
docker-compose logs -f api

# Run migrations
docker-compose exec api migrate -path migrations -database "postgres://finabbear_prod:PASSWORD@postgres:5432/finabbear_production?sslmode=disable" up

# Stop services
docker-compose down

# Stop and remove volumes
docker-compose down -v
```

---

## 🖥️ Traditional Server Deployment

### Ubuntu/Debian Server Setup

#### 1. Install Prerequisites

```bash
# Update system
sudo apt update && sudo apt upgrade -y

# Install PostgreSQL
sudo apt install postgresql postgresql-contrib -y

# Install Go
wget https://go.dev/dl/go1.25.3.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.25.3.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc

# Install migrate CLI
curl -L https://github.com/golang-migrate/migrate/releases/download/v4.17.0/migrate.linux-amd64.tar.gz | tar xvz
sudo mv migrate /usr/local/bin/
```

#### 2. Create Application User

```bash
# Create system user
sudo useradd -r -s /bin/bash -d /opt/finabbear finabbear

# Create directories
sudo mkdir -p /opt/finabbear
sudo chown finabbear:finabbear /opt/finabbear
```

#### 3. Deploy Application

```bash
# Clone repository
cd /opt/finabbear
sudo -u finabbear git clone https://github.com/your-repo/finna-bbear-be.git app
cd app

# Build application
sudo -u finabbear go build -o finabbear cmd/api/main.go

# Create .env file
sudo -u finabbear nano .env
# (Paste production configuration)

# Set permissions
sudo chmod 600 .env
```

#### 4. Create Systemd Service

**File:** `/etc/systemd/system/finabbear.service`
```ini
[Unit]
Description=FinaBBear Personal Finance API
After=network.target postgresql.service

[Service]
Type=simple
User=finabbear
Group=finabbear
WorkingDirectory=/opt/finabbear/app
ExecStart=/opt/finabbear/app/finabbear
Restart=always
RestartSec=10

# Security settings
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/finabbear

# Environment
Environment="GIN_MODE=release"

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=finabbear

[Install]
WantedBy=multi-user.target
```

#### 5. Start Service

```bash
# Reload systemd
sudo systemctl daemon-reload

# Enable service
sudo systemctl enable finabbear

# Start service
sudo systemctl start finabbear

# Check status
sudo systemctl status finabbear

# View logs
sudo journalctl -u finabbear -f
```

#### 6. Setup Nginx Reverse Proxy

**Install Nginx:**
```bash
sudo apt install nginx -y
```

**File:** `/etc/nginx/sites-available/finabbear`
```nginx
# HTTP - Redirect to HTTPS
server {
    listen 80;
    server_name api.finabbear.com;
    
    location / {
        return 301 https://$server_name$request_uri;
    }
}

# HTTPS
server {
    listen 443 ssl http2;
    server_name api.finabbear.com;

    # SSL Configuration
    ssl_certificate /etc/letsencrypt/live/api.finabbear.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/api.finabbear.com/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;
    ssl_prefer_server_ciphers on;

    # Security Headers
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-XSS-Protection "1; mode=block" always;
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;

    # Proxy to Go application
    location / {
        proxy_pass http://localhost:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection 'upgrade';
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_cache_bypass $http_upgrade;
        
        # Timeouts
        proxy_connect_timeout 60s;
        proxy_send_timeout 60s;
        proxy_read_timeout 60s;
    }

    # Rate limiting
    limit_req_zone $binary_remote_addr zone=api_limit:10m rate=10r/s;
    limit_req zone=api_limit burst=20 nodelay;

    # Logging
    access_log /var/log/nginx/finabbear_access.log;
    error_log /var/log/nginx/finabbear_error.log;
}
```

**Enable site:**
```bash
sudo ln -s /etc/nginx/sites-available/finabbear /etc/nginx/sites-enabled/
sudo nginx -t
sudo systemctl reload nginx
```

#### 7. Setup SSL with Let's Encrypt

```bash
# Install Certbot
sudo apt install certbot python3-certbot-nginx -y

# Get certificate
sudo certbot --nginx -d api.finabbear.com

# Auto-renewal is set up automatically
# Test renewal
sudo certbot renew --dry-run
```

---

## ☁️ Cloud Platform Deployment

### AWS Deployment

#### Option 1: AWS Elastic Beanstalk

1. **Install EB CLI:**
   ```bash
   pip install awsebcli
   ```

2. **Initialize EB:**
   ```bash
   eb init -p go finabbear-api
   ```

3. **Create environment:**
   ```bash
   eb create finabbear-production
   ```

4. **Deploy:**
   ```bash
   eb deploy
   ```

#### Option 2: AWS ECS with Fargate

1. **Push Docker image to ECR**
2. **Create ECS cluster**
3. **Define task definition**
4. **Create service**
5. **Configure load balancer**

### Google Cloud Platform

#### Deploy to Cloud Run

```bash
# Build and push
gcloud builds submit --tag gcr.io/PROJECT_ID/finabbear

# Deploy
gcloud run deploy finabbear \
  --image gcr.io/PROJECT_ID/finabbear \
  --platform managed \
  --region us-central1 \
  --allow-unauthenticated
```

### Heroku

```bash
# Login
heroku login

# Create app
heroku create finabbear-api

# Add PostgreSQL
heroku addons:create heroku-postgresql:hobby-dev

# Set environment variables
heroku config:set JWT_SECRET=your_secret

# Deploy
git push heroku main
```

### DigitalOcean App Platform

1. Connect GitHub repository
2. Configure build settings
3. Add environment variables
4. Deploy

---

## 🔒 Security Hardening

### Application Security

#### 1. HTTPS Only
```go
// Force HTTPS in production
if cfg.App.Env == "production" {
    router.Use(func(c *gin.Context) {
        if c.Request.Header.Get("X-Forwarded-Proto") != "https" {
            c.Redirect(http.StatusMovedPermanently, "https://"+c.Request.Host+c.Request.URL.String())
            c.Abort()
            return
        }
        c.Next()
    })
}
```

#### 2. Rate Limiting
```go
// Install: go get github.com/ulule/limiter/v3

import (
    "github.com/ulule/limiter/v3"
    "github.com/ulule/limiter/v3/drivers/store/memory"
)

// Create rate limiter
rate := limiter.Rate{
    Period: 1 * time.Minute,
    Limit:  60,
}

store := memory.NewStore()
instance := limiter.New(store, rate)
```

#### 3. CORS Configuration
```go
// Install: go get github.com/gin-contrib/cors

import "github.com/gin-contrib/cors"

config := cors.DefaultConfig()
config.AllowOrigins = []string{"https://yourdomain.com"}
config.AllowMethods = []string{"GET", "POST", "PUT", "DELETE"}
config.AllowHeaders = []string{"Authorization", "Content-Type"}
router.Use(cors.New(config))
```

#### 4. Security Headers
```go
router.Use(func(c *gin.Context) {
    c.Header("X-Frame-Options", "DENY")
    c.Header("X-Content-Type-Options", "nosniff")
    c.Header("X-XSS-Protection", "1; mode=block")
    c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
    c.Next()
})
```

### Database Security

1. **Use SSL/TLS connections**
2. **Limit network access with firewall rules**
3. **Regular security updates**
4. **Strong passwords (32+ characters)**
5. **Principle of least privilege**
6. **Regular backups**
7. **Encrypt backups**

### Server Security

```bash
# Firewall rules
sudo ufw allow 22/tcp     # SSH
sudo ufw allow 80/tcp     # HTTP
sudo ufw allow 443/tcp    # HTTPS
sudo ufw enable

# Fail2ban for SSH protection
sudo apt install fail2ban -y

# Automatic security updates
sudo apt install unattended-upgrades -y
```

---

## 📊 Monitoring & Logging

### Application Monitoring

#### 1. Health Check Endpoint
```go
router.GET("/health", func(c *gin.Context) {
    // Check database
    if err := dbpool.Ping(c.Request.Context()); err != nil {
        c.JSON(500, gin.H{"status": "unhealthy", "error": "database"})
        return
    }
    
    c.JSON(200, gin.H{
        "status": "healthy",
        "version": "1.0.0",
        "timestamp": time.Now().Unix(),
    })
})
```

#### 2. Prometheus Metrics
```bash
# Install: go get github.com/prometheus/client_golang/prometheus
```

#### 3. Application Logging
```go
// Use structured logging
logger.Info("User created",
    slog.Int64("user_id", userID),
    slog.String("username", username),
)
```

### External Monitoring Tools

- **Uptime Monitoring:** UptimeRobot, Pingdom
- **APM:** New Relic, Datadog
- **Log Management:** Loggly, Papertrail
- **Error Tracking:** Sentry

---

## 💾 Backup & Recovery

### Database Backups

#### Automated Backup Script

**File:** `/opt/finabbear/backup.sh`
```bash
#!/bin/bash

BACKUP_DIR="/opt/finabbear/backups"
DB_NAME="finabbear_production"
DB_USER="finabbear_prod"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
BACKUP_FILE="$BACKUP_DIR/backup_$TIMESTAMP.sql.gz"

# Create backup directory
mkdir -p $BACKUP_DIR

# Perform backup
PGPASSWORD=$DB_PASSWORD pg_dump -U $DB_USER -h localhost $DB_NAME | gzip > $BACKUP_FILE

# Delete backups older than 30 days
find $BACKUP_DIR -name "backup_*.sql.gz" -mtime +30 -delete

echo "Backup completed: $BACKUP_FILE"
```

#### Cron Job for Daily Backups

```bash
# Edit crontab
sudo crontab -e

# Add line (runs daily at 2 AM)
0 2 * * * /opt/finabbear/backup.sh >> /var/log/finabbear_backup.log 2>&1
```

### Restore from Backup

```bash
# Restore database
gunzip -c backup_20250101_020000.sql.gz | psql -U finabbear_prod -d finabbear_production
```

---

## 🔄 CI/CD Pipeline

### GitHub Actions Example

**File:** `.github/workflows/deploy.yml`
```yaml
name: Deploy to Production

on:
  push:
    branches: [ main ]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      
      - name: Set up Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.25.3'
      
      - name: Run tests
        run: go test -v ./...
      
      - name: Run linter
        run: go vet ./...

  deploy:
    needs: test
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      
      - name: Deploy to server
        uses: appleboy/ssh-action@master
        with:
          host: ${{ secrets.SERVER_HOST }}
          username: ${{ secrets.SERVER_USER }}
          key: ${{ secrets.SSH_PRIVATE_KEY }}
          script: |
            cd /opt/finabbear/app
            git pull
            go build -o finabbear cmd/api/main.go
            sudo systemctl restart finabbear
```

---

## 🐛 Troubleshooting

### Application Won't Start

1. **Check logs:**
   ```bash
   sudo journalctl -u finabbear -n 50
   ```

2. **Verify configuration:**
   ```bash
   cat /opt/finabbear/app/.env
   ```

3. **Test binary:**
   ```bash
   /opt/finabbear/app/finabbear
   ```

### Database Connection Issues

1. **Test connection:**
   ```bash
   psql -U finabbear_prod -d finabbear_production -h localhost
   ```

2. **Check PostgreSQL status:**
   ```bash
   sudo systemctl status postgresql
   ```

### High Memory Usage

1. **Monitor resources:**
   ```bash
   htop
   ```

2. **Check Go memory:**
   ```bash
   # Add to code for debugging
   runtime.ReadMemStats(&m)
   ```

---

## 📋 Post-Deployment Checklist

- [ ] Application starts successfully
- [ ] Health check endpoint responding
- [ ] Database migrations applied
- [ ] SSL certificate valid
- [ ] Monitoring alerts configured
- [ ] Backups running
- [ ] Security headers present
- [ ] Rate limiting active
- [ ] Logs being collected
- [ ] Documentation updated

---

## 🎉 Conclusion

Your FinaBBear API is now deployed! Monitor the application regularly and keep it updated with security patches.

For support, refer to:
- [Developer Guide](./DEVELOPER-GUIDE.md)
- [README](./README.md)
- GitHub Issues

**Happy Deploying! 🐻🚀**

