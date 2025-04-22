package database

import (
	"context"
	"errors" // สำหรับสร้าง custom errors
	"fmt"
	"log"
	"strings"
	"time"

	// --- เปลี่ยน your_module_name เป็นชื่อ Module Go ของคุณ ---
	"api-genarator/internal/models"
	// --- ---------------------------------------------------

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// --- Custom Error Types ---
// การใช้ error types เฉพาะช่วยให้ handler แยกแยะประเภทของ error ได้ง่ายขึ้น
var (
	ErrNotFound              = errors.New("document not found")
	ErrDuplicateName         = errors.New("API name already exists")
	ErrDuplicateEndpoint     = errors.New("API method and endpoint combination already exists")
	ErrDuplicateKey          = errors.New("duplicate key error during insert/update") // General duplicate error
	ErrMissingRequiredFields = errors.New("missing required fields")
	ErrUpdateFailed          = errors.New("failed to update document")
	ErrSaveFailed            = errors.New("failed to save data")
	ErrDeleteFailed          = errors.New("failed to delete data")
	ErrConfigError           = errors.New("configuration error (e.g., missing db/collection name)")
)

// Store holds the database connection and collections handles
type Store struct {
	client           *mongo.Client
	dbName           string // เก็บชื่อ DB หลักไว้เผื่อใช้
	db               *mongo.Database
	apiDefCollection *mongo.Collection
}

// NewStore creates a new database store instance
func NewStore(ctx context.Context, uri, dbName string, apiDefCollectionName string) (*Store, error) {
	if uri == "" || dbName == "" {
		return nil, fmt.Errorf("%w: MongoDB URI and Database Name cannot be empty", ErrConfigError)
	}

	clientOptions := options.Client().ApplyURI(uri).
		SetTimeout(10 * time.Second) // ตั้งค่า timeout สำหรับการเชื่อมต่อ

	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MongoDB at %s: %w", uri, err)
	}

	// ตรวจสอบการเชื่อมต่อ
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second) // Timeout สั้นๆ สำหรับ ping
	defer cancel()
	err = client.Ping(pingCtx, nil)
	if err != nil {
		// Disconnect ถ้า ping ไม่ผ่าน
		_ = client.Disconnect(context.Background()) // พยายาม disconnect แต่ไม่สน error
		return nil, fmt.Errorf("failed to ping MongoDB: %w", err)
	}
	log.Println("INFO: Successfully connected and pinged MongoDB.")

	db := client.Database(dbName)
	// TODO: ทำให้ชื่อ collection สามารถ config ได้
	apiDefCollection := db.Collection("api-definitions")

	// อาจจะสร้าง Index ที่จำเป็นตรงนี้ (ทำครั้งเดียวตอนเริ่ม หรือใช้เครื่องมือแยก)
	// createIndexes(ctx, apiDefCollection)

	return &Store{
		client:           client,
		dbName:           dbName,
		db:               db,
		apiDefCollection: apiDefCollection,
	}, nil
}

// Close disconnects the MongoDB client
func (s *Store) Close(ctx context.Context) error {
	if s.client != nil {
		log.Println("INFO: Disconnecting from MongoDB...")
		disconnectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return s.client.Disconnect(disconnectCtx)
	}
	return nil
}

// GetClient returns the underlying mongo client (use with caution)
func (s *Store) GetClient() *mongo.Client {
	return s.client
}

// GetCollection returns a handle to a specific collection in the primary database
func (s *Store) GetCollection(name string) *mongo.Collection {
	return s.db.Collection(name)
}

// --- API Definition Methods ---

// LoadAPIs loads all API definitions from the database into a map
func (s *Store) LoadAPIs(ctx context.Context) (map[string]models.ApiDefinition, error) {
	loadedRoutes := make(map[string]models.ApiDefinition)
	log.Println("INFO: Loading API definitions from database...")

	cursor, err := s.apiDefCollection.Find(ctx, bson.M{}, options.Find().SetComment("Load all API definitions"))
	if err != nil {
		log.Printf("ERROR: Error finding API definitions during load: %v", err)
		return nil, fmt.Errorf("failed to query API definitions: %w", err)
	}
	defer cursor.Close(ctx)

	loadedCount := 0
	for cursor.Next(ctx) {
		var api models.ApiDefinition
		if err := cursor.Decode(&api); err != nil {
			log.Printf("WARN: Error decoding API definition during load (ID: %s): %v", api.ID.Hex(), err) // Log ID if available
			continue                                                                                      // Skip invalid entries
		}

		// Basic validation
		if api.Method == "" || api.Endpoint == "" {
			log.Printf("WARN: Skipping API definition with empty method or endpoint (ID: %s, Name: %s)", api.ID.Hex(), api.Name)
			continue
		}

		key := api.Method + ":" + api.Endpoint
		if existing, exists := loadedRoutes[key]; exists {
			log.Printf("WARN: Duplicate route key '%s' detected during load. API Name '%s' (ID: %s) is overwriting API Name '%s' (ID: %s).",
				key, api.Name, api.ID.Hex(), existing.Name, existing.ID.Hex())
		}
		loadedRoutes[key] = api
		loadedCount++
	}

	if err := cursor.Err(); err != nil {
		log.Printf("WARN: Error during API definition cursor iteration: %v", err)
		// อาจจะไม่ใช่ critical error แต่ควร log ไว้
	}

	log.Printf("INFO: Finished loading %d API definitions.", loadedCount)
	return loadedRoutes, nil
}

// CreateAPIDefinition inserts a new API definition after validation checks
func (s *Store) CreateAPIDefinition(ctx context.Context, api *models.ApiDefinition) (primitive.ObjectID, error) {
	// 1. Validate required fields
	if api.Name == "" || api.Endpoint == "" || api.Method == "" || api.Database == "" || api.Collection == "" {
		return primitive.NilObjectID, ErrMissingRequiredFields
	}
	// TODO: Add more validation (method format, endpoint format?)

	// 2. Check for duplicate Name (atomic check if possible, otherwise best effort)
	countName, err := s.apiDefCollection.CountDocuments(ctx, bson.M{"name": api.Name}, options.Count().SetLimit(1))
	if err != nil {
		log.Printf("ERROR: Failed to check existing API name '%s': %v", api.Name, err)
		return primitive.NilObjectID, fmt.Errorf("failed to check existing API name: %w", err)
	}
	if countName > 0 {
		return primitive.NilObjectID, fmt.Errorf("%w: %s", ErrDuplicateName, api.Name)
	}

	// 3. Check for duplicate Method + Endpoint
	countEndpoint, err := s.apiDefCollection.CountDocuments(ctx, bson.M{"method": api.Method, "endpoint": api.Endpoint}, options.Count().SetLimit(1))
	if err != nil {
		log.Printf("ERROR: Failed to check existing API endpoint '%s %s': %v", api.Method, api.Endpoint, err)
		return primitive.NilObjectID, fmt.Errorf("failed to check existing API endpoint: %w", err)
	}
	if countEndpoint > 0 {
		return primitive.NilObjectID, fmt.Errorf("%w: %s %s", ErrDuplicateEndpoint, api.Method, api.Endpoint)
	}

	// 4. Prepare for insertion
	api.CreatedAt = time.Now().UTC() // Use UTC time
	api.ID = primitive.NewObjectID() // Generate ID here for consistency

	// 5. Insert
	result, err := s.apiDefCollection.InsertOne(ctx, api)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// This might happen due to race conditions if indexes enforce uniqueness differently
			log.Printf("WARN: Duplicate key error on insert for API '%s' (likely race condition): %v", api.Name, err)
			// Determine which constraint failed if possible from the error message
			if strings.Contains(err.Error(), "name_1") { // Assuming index name for name
				return primitive.NilObjectID, fmt.Errorf("%w: %s", ErrDuplicateName, api.Name)
			}
			if strings.Contains(err.Error(), "method_1_endpoint_1") { // Assuming index name for method/endpoint
				return primitive.NilObjectID, fmt.Errorf("%w: %s %s", ErrDuplicateEndpoint, api.Method, api.Endpoint)
			}
			return primitive.NilObjectID, ErrDuplicateKey
		}
		log.Printf("ERROR: Failed to insert API definition '%s': %v", api.Name, err)
		return primitive.NilObjectID, fmt.Errorf("database insert failed: %w", err)
	}

	// Check if InsertedID matches the one we generated (it should)
	if insertedID, ok := result.InsertedID.(primitive.ObjectID); !ok || insertedID != api.ID {
		log.Printf("WARN: InsertedID mismatch for API '%s'. Expected %s, Got %v", api.Name, api.ID.Hex(), result.InsertedID)
		// Still technically successful, but log it. Return the generated ID.
	}

	log.Printf("INFO: API '%s' created successfully in DB (ID: %s)", api.Name, api.ID.Hex())
	return api.ID, nil
}

// ListAPIDefinitions retrieves all API definitions
func (s *Store) ListAPIDefinitions(ctx context.Context) ([]models.ApiDefinition, error) {
	var apis []models.ApiDefinition

	cursor, err := s.apiDefCollection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{"name", 1}}).SetComment("List all API definitions")) // Sort by name
	if err != nil {
		log.Printf("ERROR: Failed to find APIs for list: %v", err)
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	defer cursor.Close(ctx)

	if err := cursor.All(ctx, &apis); err != nil {
		log.Printf("ERROR: Failed to decode API list: %v", err)
		return nil, fmt.Errorf("database decode failed: %w", err)
	}

	// Return empty slice if null, not nil slice
	if apis == nil {
		apis = []models.ApiDefinition{}
	}

	return apis, nil
}

// GetAPIDefinitionByName finds a single API definition by its unique name
func (s *Store) GetAPIDefinitionByName(ctx context.Context, name string) (*models.ApiDefinition, error) {
	var api models.ApiDefinition
	filter := bson.M{"name": name}

	err := s.apiDefCollection.FindOne(ctx, filter, options.FindOne().SetComment("Get API definition by name")).Decode(&api)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound // Return specific error for not found
		}
		log.Printf("ERROR: Failed to find API detail (name: %s): %v", name, err)
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	return &api, nil
}

// DeleteAPIDefinitionByName deletes an API definition by its name
func (s *Store) DeleteAPIDefinitionByName(ctx context.Context, name string) (int64, error) {
	filter := bson.M{"name": name}
	result, err := s.apiDefCollection.DeleteOne(ctx, filter, options.Delete().SetComment("Delete API definition by name"))
	if err != nil {
		log.Printf("ERROR: Failed to delete API definition (name: %s): %v", name, err)
		return 0, fmt.Errorf("%w: %w", ErrDeleteFailed, err)
	}

	if result.DeletedCount == 0 {
		log.Printf("WARN: No API found with name '%s' to delete.", name)
		return 0, ErrNotFound // Return not found if nothing was deleted
	}

	log.Printf("INFO: API '%s' deleted successfully from database (Count: %d)", name, result.DeletedCount)
	return result.DeletedCount, nil
}

// UpdateAPIDefinition updates an existing API definition by name
func (s *Store) UpdateAPIDefinition(ctx context.Context, name string, payload *models.ApiDefinition) (*models.ApiDefinition, error) {
	// 1. Validate payload required fields
	if payload.Endpoint == "" || payload.Method == "" || payload.Database == "" || payload.Collection == "" {
		return nil, ErrMissingRequiredFields
	}

	// 2. Get existing API to check if endpoint/method is changing and if it exists
	filter := bson.M{"name": name}
	var existingAPI models.ApiDefinition
	err := s.apiDefCollection.FindOne(ctx, filter).Decode(&existingAPI)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound // API to update doesn't exist
		}
		log.Printf("ERROR: Failed to retrieve existing API '%s' before update: %v", name, err)
		return nil, fmt.Errorf("failed to retrieve existing API: %w", err)
	}

	// 3. If Method or Endpoint changed, check for conflicts with *other* documents
	if existingAPI.Method != payload.Method || existingAPI.Endpoint != payload.Endpoint {
		conflictFilter := bson.M{
			"method":   payload.Method,
			"endpoint": payload.Endpoint,
			"_id":      bson.M{"$ne": existingAPI.ID}, // Exclude the current document
		}
		count, err := s.apiDefCollection.CountDocuments(ctx, conflictFilter, options.Count().SetLimit(1))
		if err != nil {
			log.Printf("ERROR: Failed to check for endpoint conflict during update for API '%s': %v", name, err)
			return nil, fmt.Errorf("failed to check for endpoint conflict: %w", err)
		}
		if count > 0 {
			return nil, fmt.Errorf("%w: %s %s", ErrDuplicateEndpoint, payload.Method, payload.Endpoint)
		}
	}

	// 4. Prepare update document ($set only allowed fields)
	updateFields := bson.M{
		"endpoint":        payload.Endpoint,
		"method":          payload.Method,
		"database":        payload.Database,
		"collection":      payload.Collection,
		"uniqueKey":       payload.UniqueKey, // Allow update
		"parameters":      payload.Parameters,
		"responseSchema":  payload.ResponseSchema,
		"conditionalFlow": payload.ConditionalFlow,
		"updatedAt":       time.Now().UTC(), // Add/update timestamp
	}
	update := bson.M{"$set": updateFields}

	// 5. Perform the update
	result, err := s.apiDefCollection.UpdateOne(ctx, filter, update, options.Update().SetComment("Update API definition by name"))
	if err != nil {
		// Check for duplicate key errors again (race condition on unique indexes if name could be updated, though it's not here)
		if mongo.IsDuplicateKeyError(err) {
			log.Printf("WARN: Duplicate key error on update for API '%s': %v", name, err)
			// Determine which constraint failed if possible
			if strings.Contains(err.Error(), "method_1_endpoint_1") { // Assuming index name for method/endpoint
				return nil, fmt.Errorf("%w: %s %s", ErrDuplicateEndpoint, payload.Method, payload.Endpoint)
			}
			return nil, ErrDuplicateKey
		}
		log.Printf("ERROR: Failed to update API definition (name: %s): %v", name, err)
		return nil, fmt.Errorf("%w: %w", ErrUpdateFailed, err)
	}

	if result.MatchedCount == 0 {
		// Should have been caught by FindOne earlier, but check again
		log.Printf("WARN: No API found with name '%s' during update operation (MatchedCount=0)", name)
		return nil, ErrNotFound
	}
	if result.ModifiedCount == 0 && result.MatchedCount == 1 {
		log.Printf("INFO: Update request for API '%s' matched but resulted in no changes.", name)
		// Return the existing (unchanged) data
		return &existingAPI, nil
	}

	log.Printf("INFO: API '%s' update result: Matched=%d, Modified=%d", name, result.MatchedCount, result.ModifiedCount)

	// 6. Fetch the updated document to return it
	var updatedAPI models.ApiDefinition
	err = s.apiDefCollection.FindOne(ctx, bson.M{"_id": existingAPI.ID}).Decode(&updatedAPI) // Find by ID for certainty
	if err != nil {
		log.Printf("CRITICAL: Failed to retrieve updated API data after successful update (name: %s, ID: %s): %v.", name, existingAPI.ID.Hex(), err)
		// This is problematic, the DB was updated but we can't return the result.
		return nil, fmt.Errorf("database updated, but failed to retrieve result: %w", err)
	}

	return &updatedAPI, nil
}

// --- Dynamic Data Methods ---

// getDynamicCollection returns a handle to a dynamic collection in the specified database
func (s *Store) getDynamicCollection(dbName, collName string) (*mongo.Collection, error) {
	if dbName == "" || collName == "" {
		return nil, fmt.Errorf("%w: Database and Collection names cannot be empty for dynamic operation", ErrConfigError)
	}
	// Use the same client but switch database if necessary
	return s.client.Database(dbName).Collection(collName), nil
}

// SaveData performs an upsert or insert operation on a dynamic collection
func (s *Store) SaveData(ctx context.Context, dbName, collName, uniqueKey string, data map[string]interface{}) error {
	collection, err := s.getDynamicCollection(dbName, collName)
	if err != nil {
		return err
	}

	log.Printf("DEBUG: Attempting to save data to %s.%s (UniqueKey: '%s')", dbName, collName, uniqueKey)

	// Ensure data has a timestamp? Optional
	// data["_updatedAt"] = time.Now().UTC()

	if uniqueKey != "" {
		uniqueValue, exists := data[uniqueKey]
		// Check if unique key exists AND is not nil AND not an empty string representation
		if exists && uniqueValue != nil && fmt.Sprintf("%v", uniqueValue) != "" {
			filter := bson.M{uniqueKey: uniqueValue}

			// Ensure _id is not part of the $set if it exists in data, as _id is immutable.
			// Also remove the uniqueKey field itself from $set as it's used in the filter.
			updateData := make(map[string]interface{})
			hasOtherFields := false
			for k, v := range data {
				if k != "_id" && k != uniqueKey {
					updateData[k] = v
					hasOtherFields = true
				}
			}

			// Check if there are any fields left to actually set
			if !hasOtherFields {
				log.Printf("INFO: Upsert for %v on %s.%s skipped, only key field present.", filter, dbName, collName)
				// Maybe touch an updatedAt field? If not, just return success as there's nothing to change.
				// Example: update := bson.M{"$currentDate": bson.M{"_updatedAt": true}}
				// _, err := collection.UpdateOne(ctx, filter, update, options.Update()) ... handle error ...
				return nil // Nothing to update except the key itself
			}

			update := bson.M{"$set": updateData}
			// Optional: Add $setOnInsert for fields that should only be set on creation
			// update["$setOnInsert"] = bson.M{"_createdAt": time.Now().UTC()}

			opts := options.Update().SetUpsert(true).SetComment("Save data with upsert")
			log.Printf("DEBUG: Upserting data to %s.%s with filter %v", dbName, collName, filter)
			result, err := collection.UpdateOne(ctx, filter, update, opts)
			if err != nil {
				log.Printf("ERROR: Failed to upsert data to %s.%s using UniqueKey '%s': %v", dbName, collName, uniqueKey, err)
				return fmt.Errorf("%w: upsert failed: %w", ErrSaveFailed, err)
			}
			if result.UpsertedCount > 0 {
				log.Printf("INFO: Data inserted via upsert to %s.%s with UniqueKey '%s'=%v (ID: %v)", dbName, collName, uniqueKey, uniqueValue, result.UpsertedID)
			} else if result.ModifiedCount > 0 {
				log.Printf("INFO: Data updated via upsert to %s.%s with UniqueKey '%s'=%v", dbName, collName, uniqueKey, uniqueValue)
			} else {
				log.Printf("INFO: Upsert matched document but made no changes for UniqueKey '%s'=%v in %s.%s", uniqueKey, uniqueValue, dbName, collName)
			}

		} else {
			// UniqueKey defined but value is missing/nil/empty in data -> Insert normally
			log.Printf("DEBUG: UniqueKey '%s' defined but missing/empty in data, inserting normally into %s.%s", uniqueKey, dbName, collName)
			// Add createdAt timestamp on insert?
			// data["_createdAt"] = time.Now().UTC()
			_, err := collection.InsertOne(ctx, data, options.InsertOne().SetComment("Save data via insert (unique key missing)"))
			if err != nil {
				log.Printf("ERROR: Failed to insert data (UniqueKey missing/empty) into %s.%s: %v", dbName, collName, err)
				return fmt.Errorf("%w: insert failed (unique key missing): %w", ErrSaveFailed, err)
			}
			log.Printf("INFO: Data inserted successfully (UniqueKey missing/empty) into %s.%s", dbName, collName)
		}
	} else {
		// No UniqueKey defined -> Insert normally
		log.Printf("DEBUG: No UniqueKey defined, inserting normally into %s.%s", dbName, collName)
		// Add createdAt timestamp on insert?
		// data["_createdAt"] = time.Now().UTC()
		_, err := collection.InsertOne(ctx, data, options.InsertOne().SetComment("Save data via insert (no unique key)"))
		if err != nil {
			log.Printf("ERROR: Failed to insert data (no UniqueKey) into %s.%s: %v", dbName, collName, err)
			return fmt.Errorf("%w: insert failed (no unique key): %w", ErrSaveFailed, err)
		}
		log.Printf("INFO: Data inserted successfully (no UniqueKey) into %s.%s", dbName, collName)
	}
	return nil
}

// FindData retrieves documents from a dynamic collection based on a filter
func (s *Store) FindData(ctx context.Context, dbName, collName string, filter bson.M) ([]bson.M, error) {
	collection, err := s.getDynamicCollection(dbName, collName)
	if err != nil {
		return nil, err
	}

	log.Printf("DEBUG: Finding data in %s.%s with filter: %v", dbName, collName, filter)
	var results []bson.M

	// Add options like sort, limit, projection if needed
	opts := options.Find().SetComment("Find dynamic data")

	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		log.Printf("ERROR: Failed to execute find query on %s.%s: %v", dbName, collName, err)
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	defer cursor.Close(ctx)

	if err = cursor.All(ctx, &results); err != nil {
		log.Printf("ERROR: Failed to decode find results from %s.%s: %v", dbName, collName, err)
		return nil, fmt.Errorf("database decode failed: %w", err)
	}

	// Return empty slice if null
	if results == nil {
		results = []bson.M{}
	}

	log.Printf("DEBUG: Found %d documents in %s.%s matching filter.", len(results), dbName, collName)
	return results, nil
}

// DeleteData deletes documents from a dynamic collection based on a filter
func (s *Store) DeleteData(ctx context.Context, dbName, collName string, filter bson.M) (int64, error) {
	collection, err := s.getDynamicCollection(dbName, collName)
	if err != nil {
		return 0, err
	}

	if len(filter) == 0 {
		log.Printf("WARN: Attempted to delete data from %s.%s with an empty filter. Operation aborted.", dbName, collName)
		return 0, fmt.Errorf("%w: empty filter provided for delete operation", ErrDeleteFailed)
	}

	log.Printf("DEBUG: Deleting data from %s.%s with filter: %v", dbName, collName, filter)

	// Use DeleteMany, or DeleteOne if that's more appropriate
	opts := options.Delete().SetComment("Delete dynamic data")
	result, err := collection.DeleteMany(ctx, filter, opts)
	if err != nil {
		log.Printf("ERROR: Failed to delete data from %s.%s: %v", dbName, collName, err)
		return 0, fmt.Errorf("%w: %w", ErrDeleteFailed, err)
	}

	log.Printf("INFO: Deleted %d documents from %s.%s matching filter.", result.DeletedCount, dbName, collName)
	return result.DeletedCount, nil
}

// --- Helper Functions ---

// Optional: Function to create necessary indexes on startup
// func createIndexes(ctx context.Context, apiDefCollection *mongo.Collection) {
// 	models := []mongo.IndexModel{
// 		{
// 			Keys:    bson.D{{"name", 1}},
// 			Options: options.Index().SetUnique(true).SetName("name_1"),
// 		},
// 		{
// 			Keys:    bson.D{{"method", 1}, {"endpoint", 1}},
// 			Options: options.Index().SetUnique(true).SetName("method_1_endpoint_1"),
// 		},
//      // Add other indexes as needed
// 	}

// 	opts := options.CreateIndexes().SetMaxTime(10 * time.Second)
// 	_, err := apiDefCollection.Indexes().CreateMany(ctx, models, opts)
// 	if err != nil {
// 		log.Printf("WARN: Could not create indexes for api-definitions: %v", err)
// 	} else {
// 		log.Println("INFO: Indexes for api-definitions checked/created.")
// 	}
// }
