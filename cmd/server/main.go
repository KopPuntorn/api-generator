package main

import (
	"context"
	"errors" // เพิ่ม import errors สำหรับ ErrorHandler
	"log"
	"os"
	"time"

	"api-genarator/internal/api"      // <-- เปลี่ยน dynamic-api-project เป็นชื่อโมดูลของคุณ
	"api-genarator/internal/database" // <-- เปลี่ยน dynamic-api-project เป็นชื่อโมดูลของคุณ
	"api-genarator/internal/models"   // <-- เปลี่ยน dynamic-api-project เป็นชื่อโมดูลของคุณ

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors" // Add this import
	"github.com/gofiber/fiber/v2/middleware/recover"
	// "github.com/gofiber/fiber/v2/middleware/logger" // ย้ายไปใส่ใน routes.go หรือใส่ที่นี่ก็ได้
	"os/signal"
    "syscall"
)

func main() {
	// --- Configuration ---
	// Consider adding a configuration file option in addition to environment variables
	// For example, you could check for a config.json file first, then fall back to env vars
	mongoURI := os.Getenv("MONGO_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017"
		log.Printf("WARN: MONGO_URI environment variable not set, using default: %s", mongoURI)
	}
	dbName := os.Getenv("MONGO_DB_NAME")
	if dbName == "" {
		dbName = "dynamic-api-db"
		log.Printf("WARN: MONGO_DB_NAME environment variable not set, using default: %s", dbName)
	}
	apiDefCollectionName := os.Getenv("MONGO_API_DEF_COLLECTION")
	if apiDefCollectionName == "" {
		apiDefCollectionName = "api-definitions"
		log.Printf("WARN: MONGO_API_DEF_COLLECTION environment variable not set, using default: %s", apiDefCollectionName)
	}
	serverPort := os.Getenv("SERVER_PORT")
	if serverPort == "" {
		serverPort = "5000"
	}
	listenAddr := ":" + serverPort

	// --- Database Connection ---
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // เพิ่มเวลา timeout เล็กน้อย
	defer cancel()

	store, err := database.NewStore(ctx, mongoURI, dbName, apiDefCollectionName)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize database store: %v", err)
	}
	defer func() {
		log.Println("INFO: Closing database connection...")
		if err := store.Close(context.Background()); err != nil {
			log.Printf("ERROR: Failed to close database connection: %v", err)
		}
	}()

	// --- Load Initial APIs ---
	loadCtx, loadCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer loadCancel()
	initialAPIs, err := store.LoadAPIs(loadCtx)
	if err != nil {
		log.Printf("ERROR: Failed to load initial APIs: %v. Server starting with potentially empty routes.", err)
		if initialAPIs == nil {
			initialAPIs = make(map[string]models.ApiDefinition) // Ensure map is not nil
		}
	}

	// --- Initialize Handler ---
	apiHandler := api.NewHandler(store, initialAPIs)

	// --- Create Fiber App ---
	app := fiber.New(fiber.Config{
		BodyLimit: 10 * 1024 * 1024, // 10 MB
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			message := "An unexpected error occurred"

			var e *fiber.Error
			if errors.As(err, &e) {
				code = e.Code
				message = e.Message
			}

			// Log the full error internally for debugging
			log.Printf("ERROR Handler: Path=%s, Error=%v", c.Path(), err)

			// Send JSON error response
			// Avoid sending detailed internal errors to the client in production
			return c.Status(code).JSON(fiber.Map{
				"error": message,
			})
		},
	})

	// --- Middleware ---
	app.Use(recover.New()) // Recover from panics

	// Add CORS middleware
	app.Use(cors.New(cors.Config{
		AllowOrigins:     "*", // Allow all origins
		AllowMethods:     "GET,POST,PUT,DELETE,OPTIONS,PATCH",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization",
		AllowCredentials: false, // Set to false when using wildcard origin
		ExposeHeaders:    "Content-Length",
		MaxAge:           86400, // 24 hours
	}))

	// --- Register Routes ---
	api.RegisterRoutes(app, apiHandler) // Pass the app and handler

	// --- Start Server ---
	log.Printf("INFO: Starting Fiber server on address %s", listenAddr)
	if err := app.Listen(listenAddr); err != nil {
		log.Fatalf("FATAL: Failed to start server: %v", err)
	}

	// --- Graceful Shutdown ---
	// Add graceful shutdown handling with OS signals
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	
	go func() {
		<-c
		log.Println("INFO: Graceful shutdown initiated...")
		// Give active connections time to finish
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		
		if err := app.ShutdownWithContext(ctx); err != nil {
			log.Printf("ERROR: Server shutdown failed: %v", err)
		}
		
		log.Println("INFO: Server shutdown complete")
		os.Exit(0)
	}()
}
