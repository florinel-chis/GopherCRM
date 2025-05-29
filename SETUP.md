# GoCRM Setup Guide

This guide will help you set up and run both the backend and frontend of the GoCRM application.

## Backend Setup

### 1. Prerequisites
- Go 1.21 or higher
- MySQL 8.0 or higher
- Git

### 2. Database Setup
```bash
# Create the database
make create-db
# or manually:
mysql -u root -p < scripts/create_database.sql
```

### 3. Environment Configuration
```bash
# Copy the example environment file
cp .env.example .env

# Edit .env with your configuration
# Key settings:
# - DB_USER (your MySQL username)
# - DB_PASS (your MySQL password)
# - DB_NAME (should be 'gocrm')
# - SERVER_PORT (default: 8080)
# - JWT_SECRET (generate a secure random string)
```

### 4. Install Dependencies
```bash
make deps
# or
go mod download
```

### 5. Run the Backend
```bash
make run
# or
go run cmd/main.go
```

The backend API will be available at `http://localhost:8080`

### 6. Create Admin User
In a new terminal:
```bash
make create-admin
# Follow the prompts to create an admin user
```

## Frontend Setup

### 1. Prerequisites
- Node.js 18 or higher
- npm or yarn

### 2. Navigate to Frontend Directory
```bash
cd gocrm-ui
```

### 3. Install Dependencies
```bash
npm install
```

### 4. Configure API URL
```bash
# Copy the example environment file
cp .env.example .env

# The default configuration should work:
# VITE_API_BASE_URL=http://localhost:8080/api
```

### 5. Run the Frontend
```bash
npm run dev
```

The frontend will be available at `http://localhost:5173`

## Running Both Together

### Option 1: Two Terminal Windows
Terminal 1 (Backend):
```bash
cd /path/to/gocrm
make run
```

Terminal 2 (Frontend):
```bash
cd /path/to/gocrm/gocrm-ui
npm run dev
```

### Option 2: Using a Process Manager
Create a `Procfile`:
```
backend: cd /path/to/gocrm && make run
frontend: cd /path/to/gocrm/gocrm-ui && npm run dev
```

Then use a tool like `foreman` or `overmind`:
```bash
foreman start
```

## Testing the Setup

### 1. Check Backend Health
```bash
curl http://localhost:8080/health
```

### 2. Login via Frontend
1. Open http://localhost:5173
2. Login with the admin credentials you created
3. You should see the dashboard

### 3. Test API Directly
```bash
# Login
curl -X POST http://localhost:8080/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@example.com","password":"yourpassword"}'

# Use the returned token for authenticated requests
curl http://localhost:8080/api/users/me \
  -H "Authorization: Bearer YOUR_TOKEN_HERE"
```

## Common Issues

### Backend Issues

1. **Database connection failed**
   - Ensure MySQL is running
   - Check database credentials in .env
   - Verify database exists: `mysql -u root -p -e "SHOW DATABASES;"`

2. **Port already in use**
   - Change SERVER_PORT in .env
   - Or kill the process: `lsof -ti:8080 | xargs kill`

3. **CORS errors**
   - Frontend URL is already included in CORS config
   - For production, update `internal/middleware/cors.go`

### Frontend Issues

1. **API connection failed**
   - Ensure backend is running
   - Check VITE_API_BASE_URL in .env
   - Verify no proxy issues

2. **Login not working**
   - Check browser console for errors
   - Ensure you created an admin user
   - Verify JWT_SECRET matches between frontend/backend

## Production Deployment

### Backend
1. Set `SERVER_MODE=production` in .env
2. Use a reverse proxy (nginx, caddy)
3. Set up SSL certificates
4. Use a process manager (systemd, supervisor)

### Frontend
1. Build for production: `npm run build`
2. Serve the `dist` folder with a web server
3. Configure your web server to handle client-side routing

## API Documentation

The API endpoints follow RESTful conventions:

- Auth: `/api/auth/*`
- Users: `/api/users/*`
- Leads: `/api/leads/*`
- Customers: `/api/customers/*`
- Tickets: `/api/tickets/*`
- Tasks: `/api/tasks/*`

All protected endpoints require an `Authorization: Bearer <token>` header.