# GopherCRM Frontend

A modern React TypeScript frontend for the GopherCRM application, built with Material-UI components and powered by Vite.

## Features

- 🎨 **Modern UI**: Built with Material-UI (MUI) components
- ⚡ **Fast Development**: Powered by Vite with Hot Module Replacement
- 🔒 **Authentication**: JWT-based authentication with role-based access control
- 📱 **Responsive Design**: Mobile-first responsive design
- 🧪 **Testing**: Comprehensive test suite with Vitest and React Testing Library
- 🔗 **API Integration**: TanStack Query for efficient data fetching and caching
- 🎯 **TypeScript**: Full type safety throughout the application

## Tech Stack

- **React 18** - UI framework
- **TypeScript** - Type safety
- **Material-UI (MUI)** - Component library and design system
- **React Router** - Client-side routing
- **TanStack Query** - Data fetching and caching
- **Vite** - Build tool and development server
- **Vitest** - Testing framework
- **React Testing Library** - Component testing utilities

## Prerequisites

- Node.js 18+ and npm/yarn
- Backend GopherCRM server running on port 8080

## Getting Started

### Installation

```bash
# Install dependencies
npm install

# Or using yarn
yarn install
```

### Development

```bash
# Start development server
npm run dev

# Or using yarn
yarn dev
```

The application will be available at `http://localhost:5173`

### Building for Production

```bash
# Build for production
npm run build

# Preview production build
npm run preview
```

### Testing

```bash
# Run tests
npm run test

# Run tests in watch mode
npm run test:watch

# Run tests with coverage
npm run test:coverage
```

### Code Quality

```bash
# Run ESLint
npm run lint

# Fix ESLint issues
npm run lint:fix

# Type checking
npm run type-check
```

## Project Structure

```
src/
├── api/                    # API client and endpoints
│   ├── client.ts          # HTTP client configuration
│   └── endpoints/         # API endpoint definitions
├── components/            # Reusable UI components
│   ├── form/             # Form-specific components
│   └── *.tsx             # Component files
├── contexts/             # React contexts
│   ├── AuthContext.tsx   # Authentication context
│   ├── ConfigurationContext.tsx # Configuration management
│   └── SnackbarContext.tsx # Notification system
├── hooks/                # Custom React hooks
├── layouts/              # Page layout components
├── pages/                # Application pages/routes
│   ├── auth/             # Authentication pages
│   ├── customers/        # Customer management
│   ├── leads/            # Lead management
│   ├── settings/         # Settings pages
│   ├── tasks/            # Task management
│   ├── tickets/          # Ticket system
│   └── users/            # User management
├── routes/               # Routing configuration
├── theme/                # Material-UI theme configuration
├── types/                # TypeScript type definitions
└── utils/                # Utility functions
```

## Key Components

### Authentication
- JWT-based authentication with automatic token refresh
- Role-based access control (Admin, Sales, Support, Customer)
- Protected routes based on user permissions

### Dashboard
- Real-time statistics from backend API
- Quick action buttons for common tasks
- Responsive stat cards showing:
  - Total leads and customers
  - Open tickets and pending tasks
  - Lead conversion rate

### Data Management
- **Leads**: Lead tracking and conversion to customers
- **Customers**: Customer lifecycle management
- **Tickets**: Support ticket system with assignments
- **Tasks**: Task management with due dates and priorities
- **Users**: User management (admin only)

### Configuration Management
- Dynamic configuration system
- Category-based settings organization
- Real-time updates without restart
- Type-safe configuration editing

## API Integration

The frontend communicates with the GopherCRM backend through RESTful APIs:

### Authentication Endpoints
- `POST /api/v1/auth/login` - User authentication
- `POST /api/v1/auth/register` - User registration

### Data Endpoints
- Leads: `/api/v1/leads/*`
- Customers: `/api/v1/customers/*`
- Tickets: `/api/v1/tickets/*`
- Tasks: `/api/v1/tasks/*`
- Users: `/api/v1/users/*`
- Configurations: `/api/v1/configurations/*`
- Dashboard: `/api/v1/dashboard/stats`

### Error Handling
- Consistent error response format
- User-friendly error messages
- Automatic retry for failed requests
- Loading states and error boundaries

## Development Guidelines

### Code Style
- Use TypeScript for all new files
- Follow React functional component patterns
- Use Material-UI components and theme system
- Implement proper error boundaries
- Write tests for complex logic

### Testing
- Unit tests for utilities and hooks
- Component tests for UI interactions
- Integration tests for API calls
- Maintain test coverage above 80%

### Performance
- Lazy loading for route components
- Efficient API data caching with TanStack Query
- Optimized bundle splitting
- Image optimization

## Configuration

### Environment Variables
The application uses environment variables for configuration:

```env
VITE_API_BASE_URL=http://localhost:8080  # Backend API URL
```

### Theme Customization
Customize the Material-UI theme in `src/theme/index.ts`:

```typescript
export const theme = createTheme({
  palette: {
    primary: {
      main: '#1976d2',
    },
    // ... other theme options
  },
});
```

## Deployment

### Production Build
```bash
npm run build
```

The build output will be in the `dist/` directory, ready for deployment to any static hosting service.

### Docker Deployment
```dockerfile
FROM nginx:alpine
COPY dist/ /usr/share/nginx/html
EXPOSE 80
```

## Contributing

1. Follow the existing code style and patterns
2. Write tests for new features
3. Update documentation as needed
4. Ensure TypeScript compilation passes
5. Test responsiveness across devices

## Troubleshooting

### Common Issues

**Build Errors**
- Check TypeScript errors: `npm run type-check`
- Clear node_modules and reinstall dependencies

**API Connection Issues**
- Verify backend server is running on port 8080
- Check CORS configuration in backend
- Confirm API endpoints in network tab

**Authentication Issues**
- Clear localStorage tokens
- Check JWT token expiration
- Verify backend authentication middleware

## Browser Support

- Chrome (latest)
- Firefox (latest)
- Safari (latest)
- Edge (latest)

## License

This project is part of the GopherCRM system and follows the same license terms.