package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"syscall"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"golang.org/x/term"
)

func main() {
	// Define flags
	var (
		email     = flag.String("email", "", "Admin email address")
		name      = flag.String("name", "", "Admin full name")
		nonInteractive = flag.Bool("non-interactive", false, "Run in non-interactive mode")
		password  = flag.String("password", "", "Admin password (only for non-interactive mode)")
	)
	flag.Parse()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Initialize database
	if err := models.InitDatabase(&cfg.Database); err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	// Run migrations
	if err := models.MigrateDatabase(); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}

	// Create user repository and service
	userRepo := repository.NewUserRepository(models.DB)
	userService := service.NewUserService(userRepo)

	// Collect user details
	var adminEmail, adminName, adminPassword string

	if *nonInteractive {
		// Non-interactive mode: all values must be provided via flags
		if *email == "" || *name == "" || *password == "" {
			log.Fatal("In non-interactive mode, --email, --name, and --password flags are required")
		}
		adminEmail = *email
		adminName = *name
		adminPassword = *password
	} else {
		// Interactive mode
		reader := bufio.NewReader(os.Stdin)

		// Get email
		if *email != "" {
			adminEmail = *email
			fmt.Printf("Email: %s\n", adminEmail)
		} else {
			fmt.Print("Enter admin email: ")
			adminEmail, _ = reader.ReadString('\n')
			adminEmail = strings.TrimSpace(adminEmail)
		}

		// Get name
		if *name != "" {
			adminName = *name
			fmt.Printf("Name: %s\n", adminName)
		} else {
			fmt.Print("Enter admin name: ")
			adminName, _ = reader.ReadString('\n')
			adminName = strings.TrimSpace(adminName)
		}

		// Get password
		fmt.Print("Enter admin password: ")
		passwordBytes, err := term.ReadPassword(int(syscall.Stdin))
		if err != nil {
			log.Fatalf("Failed to read password: %v", err)
		}
		adminPassword = string(passwordBytes)
		fmt.Println() // New line after password

		// Confirm password
		fmt.Print("Confirm password: ")
		confirmBytes, err := term.ReadPassword(int(syscall.Stdin))
		if err != nil {
			log.Fatalf("Failed to read password confirmation: %v", err)
		}
		fmt.Println() // New line after password

		if adminPassword != string(confirmBytes) {
			log.Fatal("Passwords do not match")
		}
	}

	// Validate inputs
	if adminEmail == "" || adminName == "" || adminPassword == "" {
		log.Fatal("Email, name, and password are required")
	}

	if len(adminPassword) < 8 {
		log.Fatal("Password must be at least 8 characters long")
	}

	// Parse name into first and last name
	nameParts := strings.Fields(adminName)
	var firstName, lastName string
	if len(nameParts) > 0 {
		firstName = nameParts[0]
	}
	if len(nameParts) > 1 {
		lastName = strings.Join(nameParts[1:], " ")
	} else {
		lastName = "" // Optional: could be the same as firstName
	}

	// Create admin user
	adminUser := &models.User{
		Email:     adminEmail,
		FirstName: firstName,
		LastName:  lastName,
		Role:      models.RoleAdmin,
		IsActive:  true,
	}

	// Register the user
	err = userService.Register(adminUser, adminPassword)
	if err != nil {
		log.Fatalf("Failed to create admin user: %v", err)
	}

	fmt.Printf("\n✅ Admin user created successfully!\n")
	fmt.Printf("   Email: %s\n", adminUser.Email)
	fmt.Printf("   Name: %s\n", adminUser.FullName())
	fmt.Printf("   Role: %s\n", adminUser.Role)
	fmt.Printf("\nYou can now login with these credentials.\n")
}