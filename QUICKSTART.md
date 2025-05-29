# GoCRM Quick Start Guide

Everything has been prepared for you! Here are the commands to run the application:

## ✅ Already Completed
- Backend .env file created with default configuration
- Frontend .env file configured 
- All Go dependencies installed
- All npm dependencies installed
- MySQL database 'gocrm' created
- create-admin tool built

## 🚀 Start the Applications

### 1. Start Backend (Terminal 1)
```bash
cd /Users/flo/fch/gocrm
make run
```
Or:
```bash
cd /Users/flo/fch/gocrm
go run cmd/main.go
```

The backend will:
- Connect to MySQL database
- Run automatic migrations (create tables)
- Start on http://localhost:8080
- API available at http://localhost:8080/api

### 2. Start Frontend (Terminal 2)
```bash
cd /Users/flo/fch/gocrm/gocrm-ui
npm run dev
```

The frontend will:
- Start Vite dev server
- Available at http://localhost:5173
- Hot reload enabled

### 3. Create Admin User (Terminal 3)
```bash
cd /Users/flo/fch/gocrm
make create-admin
```

Follow the prompts:
- Enter admin email
- Enter admin name
- Enter password (min 8 characters)
- Confirm password

## 🔍 Verify Everything Works

1. **Check Backend Health**
   ```bash
   curl http://localhost:8080/health
   ```

2. **Open Frontend**
   - Browse to http://localhost:5173
   - Login with your admin credentials

3. **Check Logs**
   - Backend logs appear in Terminal 1
   - Frontend logs appear in Terminal 2

## 📋 Summary of URLs
- Frontend: http://localhost:5173
- Backend API: http://localhost:8080/api
- Health Check: http://localhost:8080/health

## 🛠 Troubleshooting

If MySQL connection fails:
- Check MySQL is running: `mysql -u root -e "SELECT 1;"`
- Check password in .env file if you have one set

If port 8080 is in use:
- Change SERVER_PORT in .env
- Or kill the process: `lsof -ti:8080 | xargs kill`

If frontend can't connect to backend:
- Ensure backend is running first
- Check browser console for errors
- CORS is already configured