package main

import (
	"bufio"
	"errors"
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
	// run() owns every failure so its deferred close always runs: log.Fatal
	// exits the process without unwinding the stack, which on SQLite would
	// leave the write-ahead log un-checkpointed next to the database file.
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() (err error) {
	// Define flags
	var (
		email          = flag.String("email", "", "Admin email address")
		name           = flag.String("name", "", "Admin full name")
		nonInteractive = flag.Bool("non-interactive", false, "Run in non-interactive mode")
		password       = flag.String("password", "", "Admin password (only for non-interactive mode)")
	)
	flag.Parse()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Initialize database
	if err := models.InitDatabase(&cfg.Database); err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	// A failing close is a real failure — on SQLite it means the write-ahead log
	// was not checkpointed — so it becomes the command's exit status instead of a
	// log line the caller cannot see. An error already on its way out wins: it is
	// the cause, and the close failure is usually its consequence.
	defer func() {
		if closeErr := models.CloseDatabase(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close database: %w", closeErr)
		}
	}()

	// Run migrations
	if err := models.MigrateDatabase(); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	// Create user repository and service
	userRepo := repository.NewUserRepository(models.DB)
	userService := service.NewUserService(userRepo)

	// Collect user details
	var adminEmail, adminName, adminPassword string

	if *nonInteractive {
		// Non-interactive mode: all values must be provided via flags
		if *email == "" || *name == "" || *password == "" {
			return errors.New("in non-interactive mode, --email, --name, and --password flags are required")
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
			return fmt.Errorf("failed to read password: %w", err)
		}
		adminPassword = string(passwordBytes)
		fmt.Println() // New line after password

		// Confirm password
		fmt.Print("Confirm password: ")
		confirmBytes, err := term.ReadPassword(int(syscall.Stdin))
		if err != nil {
			return fmt.Errorf("failed to read password confirmation: %w", err)
		}
		fmt.Println() // New line after password

		if adminPassword != string(confirmBytes) {
			return errors.New("passwords do not match")
		}
	}

	// Validate inputs
	if adminEmail == "" || adminName == "" || adminPassword == "" {
		return errors.New("email, name, and password are required")
	}

	if len(adminPassword) < 8 {
		return errors.New("password must be at least 8 characters long")
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

	// Register the user. gocrm-ui/e2e/global-setup.ts matches /already exists/i
	// over this command's output to tell "admin was already seeded" apart from a
	// real failure, so the duplicate-account wording must survive rewording here
	// and in service.Register.
	if err := userService.Register(adminUser, adminPassword); err != nil {
		return fmt.Errorf("failed to create admin user: %w", err)
	}

	fmt.Printf("\n✅ Admin user created successfully!\n")
	fmt.Printf("   Email: %s\n", adminUser.Email)
	fmt.Printf("   Name: %s\n", adminUser.FullName())
	fmt.Printf("   Role: %s\n", adminUser.Role)
	fmt.Printf("\nYou can now login with these credentials.\n")

	return nil
}
