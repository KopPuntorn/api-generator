package api

import (
	"context"
	"errors" // Import errors package for errors.As
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	// --- เปลี่ยน your_module_name เป็นชื่อ Module Go ของคุณ ---
	"api-genarator/internal/core"
	"api-genarator/internal/database"
	"api-genarator/internal/models"

	// --- ---------------------------------------------------

	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	// "go.mongodb.org/mongo-driver/mongo" // อาจจะไม่จำเป็นต้องใช้ mongo โดยตรงใน handler แล้ว
)

// Handler holds dependencies for API handlers
type Handler struct {
	store         *database.Store
	dynamicRoutes map[string]models.ApiDefinition // In-memory cache
	routesMutex   sync.RWMutex                    // Mutex for the cache
}

// NewHandler creates a new API handler
func NewHandler(store *database.Store, initialRoutes map[string]models.ApiDefinition) *Handler {
	if initialRoutes == nil {
		initialRoutes = make(map[string]models.ApiDefinition)
	}
	return &Handler{
		store:         store,
		dynamicRoutes: initialRoutes,
	}
}

// --- API Definition CRUD Handlers ---

// CreateAPI handles the creation of a new API definition
func (h *Handler) CreateAPI(c *fiber.Ctx) error {
	var api models.ApiDefinition

	// 1. Parse request body
	if err := c.BodyParser(&api); err != nil {
		log.Printf("WARN: Cannot parse JSON for CreateAPI: %v", err)
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{
			"status":  "error",
			"code":    http.StatusBadRequest,
			"message": "Cannot parse JSON",
		})
	}

	// 2. Call database layer to create
	ctx, cancel := context.WithTimeout(c.Context(), 10*time.Second) // Use Fiber context
	defer cancel()

	// CreateAPIDefinition ใน store ควรคืน error ที่เฉพาะเจาะจงมากขึ้น
	insertedID, err := h.store.CreateAPIDefinition(ctx, &api) // Pass pointer to potentially get ID back
	if err != nil {
		log.Printf("ERROR: Handler failed to create API '%s': %v", api.Name, err)
		// ตรวจสอบ error ที่เฉพาะเจาะจงจาก Store layer
		if errors.Is(err, database.ErrMissingRequiredFields) { // สมมติว่ามี error type นี้ใน database package
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if errors.Is(err, database.ErrDuplicateName) || errors.Is(err, database.ErrDuplicateEndpoint) || errors.Is(err, database.ErrDuplicateKey) { // สมมติว่ามี error type เหล่านี้
			return c.Status(http.StatusConflict).JSON(fiber.Map{"error": err.Error()})
		}
		// Fallback error
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to save API definition"})
	}
	api.ID = insertedID // Ensure ID is set from return value

	// 3. Update cache (Write Lock)
	key := api.Method + ":" + api.Endpoint
	h.routesMutex.Lock()
	h.dynamicRoutes[key] = api
	h.routesMutex.Unlock()
	log.Printf("INFO: Added/Updated route key '%s' in cache for API '%s'", key, api.Name)

	// 4. Return response
	return c.Status(http.StatusCreated).JSON(fiber.Map{
		"status":  "success",
		"code":    http.StatusCreated,
		"message": "API created successfully",
		"data":    api,
	})
}

// ListAPIs handles listing all API definitions
func (h *Handler) ListAPIs(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.Context(), 10*time.Second)
	defer cancel()

	apis, err := h.store.ListAPIDefinitions(ctx)
	if err != nil {
		log.Printf("ERROR: Handler failed to list APIs: %v", err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{
			"status":  "error",
			"code":    http.StatusInternalServerError,
			"message": "Failed to retrieve API list",
		})
	}

	if apis == nil {
		apis = []models.ApiDefinition{}
	}

	return c.Status(http.StatusOK).JSON(fiber.Map{
		"status": "success",
		"code":   http.StatusOK,
		"data":   apis,
	})
}

// GetAPIDetail handles retrieving a single API definition by name
func (h *Handler) GetAPIDetail(c *fiber.Ctx) error {
	name := c.Params("name")
	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
	defer cancel()

	// สมมติว่า GetAPIDefinitionByName คืน pointer หรือ nil และ error
	api, err := h.store.GetAPIDefinitionByName(ctx, name)
	if err != nil {
		log.Printf("ERROR: Handler failed to get API detail (name: %s): %v", name, err)
		// ไม่ควรคืน mongo.ErrNoDocuments ให้ client โดยตรง
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to retrieve API detail"})
	}
	if api == nil { // ตรวจสอบว่า store คืน nil หรือไม่เมื่อไม่เจอ
		log.Printf("INFO: API detail not found in handler (name: %s)", name)
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "API not found"})
	}

	return c.JSON(api)
}

// DeleteAPI handles deleting an API definition by name
func (h *Handler) DeleteAPI(c *fiber.Ctx) error {
	name := c.Params("name")
	if name == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "API name parameter is required"})
	}

	ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
	defer cancel()

	// 1. Get API details first to know which key to remove from cache
	// ใช้ GetAPIDefinitionByName ที่มีอยู่แล้ว
	apiToDelete, err := h.store.GetAPIDefinitionByName(ctx, name)
	if err != nil {
		log.Printf("ERROR: Handler failed find API for deletion (name: %s): %v", name, err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to retrieve API data before deletion"})
	}
	if apiToDelete == nil {
		log.Printf("WARN: API not found for deletion in handler (name: %s)", name)
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "API not found"})
	}
	keyToDelete := apiToDelete.Method + ":" + apiToDelete.Endpoint

	// 2. Call database layer to delete
	// สมมติว่า DeleteAPIDefinitionByName คืนจำนวนที่ลบ แะละ error
	deletedCount, err := h.store.DeleteAPIDefinitionByName(ctx, name)
	if err != nil {
		log.Printf("ERROR: Handler failed to delete API (name: %s): %v", name, err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to delete API definition"})
	}
	if deletedCount == 0 {
		// ควรถูกจับได้โดย GetAPIDefinitionByName แต่ตรวจสอบอีกครั้ง
		log.Printf("WARN: API '%s' not found during delete operation (Store returned 0)", name)
		// อาจะยังคืน NotFound เพราะ GetAPIDefinitionByName ไม่เจอตั้งแต่แรก หรืออาจมี race condition
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "API not found during delete operation"})
	}
	log.Printf("INFO: API '%s' deleted successfully from database", name)

	// 3. Remove from cache (Write Lock)
	h.routesMutex.Lock()
	delete(h.dynamicRoutes, keyToDelete)
	h.routesMutex.Unlock()
	log.Printf("INFO: Removed route key '%s' from cache for deleted API '%s'", keyToDelete, name)

	// 4. Return response
	return c.Status(http.StatusOK).JSON(fiber.Map{"message": "API deleted successfully"})
}

// UpdateAPI handles updating an API definition by name
func (h *Handler) UpdateAPI(c *fiber.Ctx) error {
	name := c.Params("name")
	if name == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "API name parameter is required"})
	}

	// 1. Parse payload
	var payloadToUpdate models.ApiDefinition
	if err := c.BodyParser(&payloadToUpdate); err != nil {
		log.Printf("WARN: Cannot parse JSON for UpdateAPI (name: %s): %v", name, err)
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Cannot parse JSON"})
	}

	ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
	defer cancel()

	// 2. Get existing API to find the old cache key
	// (ทำภายใน store.UpdateAPIDefinition หรือเรียก Get ก่อนก็ได้)
	existingAPI, err := h.store.GetAPIDefinitionByName(ctx, name)
	if err != nil {
		log.Printf("ERROR: Handler failed find existing API for update (name: %s): %v", name, err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to retrieve existing API data for update"})
	}
	if existingAPI == nil {
		log.Printf("WARN: API not found for update in handler (name: %s)", name)
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "API not found for update"})
	}
	oldKey := existingAPI.Method + ":" + existingAPI.Endpoint

	// 3. Call database layer to update
	// สมมติว่า UpdateAPIDefinition คืน *models.ApiDefinition ที่อัปเดตแล้ว แะละ error
	updatedAPI, err := h.store.UpdateAPIDefinition(ctx, name, &payloadToUpdate)
	if err != nil {
		log.Printf("ERROR: Handler failed to update API (name: %s): %v", name, err)
		if errors.Is(err, database.ErrMissingRequiredFields) { // สมมติมี error type นี้
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		if errors.Is(err, database.ErrNotFound) { // สมมติมี error type นี้ ถ้า update แล้ว MatchedCount = 0
			return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "API not found during update"})
		}
		// Check for duplicate endpoint error if method/endpoint changed and conflicts
		if errors.Is(err, database.ErrDuplicateEndpoint) {
			return c.Status(http.StatusConflict).JSON(fiber.Map{"error": err.Error()})
		}
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update API definition"})
	}
	// Store ควรคืน error ถ้า update แล้วหา document ที่อัปเดตกลับมาไม่ได้
	if updatedAPI == nil {
		log.Printf("CRITICAL: Update successful for API '%s' but retrieval of updated doc failed.", name)
		// สถานการณ์นี้ไม่ควรเกิดถ้า Store ทำงานถูกต้อง
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{
			"error":   "API updated in DB, but failed to retrieve updated data for cache",
			"warning": "The API route cache might be temporarily inconsistent.",
		})
	}

	// 4. Update cache (Write Lock)
	newKey := updatedAPI.Method + ":" + updatedAPI.Endpoint
	h.routesMutex.Lock()
	if oldKey != newKey && oldKey != "" { // Remove old key if it changed
		delete(h.dynamicRoutes, oldKey)
		log.Printf("INFO: Removed old route key '%s' from cache for API '%s'", oldKey, name)
	}
	h.dynamicRoutes[newKey] = *updatedAPI // Add/Update with new key/data
	h.routesMutex.Unlock()
	log.Printf("INFO: API '%s' updated successfully in cache (New Key: '%s')", name, newKey)

	// 5. Return response
	return c.JSON(fiber.Map{
		"message": "API updated successfully",
		"api":     updatedAPI,
	})
}

// --- Dynamic Route Handler ---

// Helper function to convert array-style response to map
func convertArrayToMap(data interface{}) interface{} {
	log.Printf("DEBUG: convertArrayToMap input type: %T, value: %v", data, data)

	// ตรวจสอบกรณีที่ข้อมูลเป็น nil
	if data == nil {
		return data
	}

	// ตรวจสอบหลายรูปแบบของ array
	var slice []interface{}

	// กรณีที่ 1: เป็น []interface{} โดยตรง
	if s, ok := data.([]interface{}); ok {
		slice = s
		log.Printf("DEBUG: Detected []interface{} type")
	} else if s, ok := data.([]map[string]interface{}); ok {
		// กรณีที่ 2: เป็น []map[string]interface{}
		slice = make([]interface{}, len(s))
		for i, m := range s {
			slice[i] = m
		}
		log.Printf("DEBUG: Converted []map[string]interface{} to []interface{}")
	} else {
		// ตรวจสอบประเภทข้อมูลด้วย reflection
		dataType := fmt.Sprintf("%T", data)
		log.Printf("DEBUG: Data type is: %s", dataType)

		// ถ้าไม่ใช่ array หรือ slice ให้คืนค่าเดิม
		if dataType[:2] != "[]" {
			return data
		}

		// พยายามแปลงเป็น slice ด้วยวิธีอื่น (อาจต้องปรับตามข้อมูลจริง)
		log.Printf("DEBUG: Data appears to be a slice but type assertion failed, returning as-is")
		return data
	}

	// ถ้า slice ว่างเปล่า ให้คืนค่าเดิม
	if len(slice) == 0 {
		return data
	}

	// ตรวจสอบว่าเป็นรูปแบบ Key-Value หรือไม่
	isKeyValueFormat := true
	for _, item := range slice {
		m, ok := item.(map[string]interface{})
		if !ok {
			log.Printf("DEBUG: Item is not a map: %T %v", item, item)
			isKeyValueFormat = false
			break
		}

		// ตรวจสอบว่ามี Key และ Value หรือไม่
		hasKey := false
		hasValue := false

		if _, ok := m["Key"]; ok {
			hasKey = true
		} else if _, ok := m["key"]; ok {
			hasKey = true
		}

		if _, ok := m["Value"]; ok {
			hasValue = true
		} else if _, ok := m["value"]; ok {
			hasValue = true
		}

		if !hasKey || !hasValue {
			log.Printf("DEBUG: Item doesn't have Key/Value fields: %v", m)
			isKeyValueFormat = false
			break
		}
	}

	if !isKeyValueFormat {
		log.Printf("DEBUG: Data is not in Key-Value format, returning as-is")
		return data
	}

	// แปลงเป็น map
	result := make(map[string]interface{})
	for _, item := range slice {
		m := item.(map[string]interface{})
		var key string
		var value interface{}

		// ดึงค่า key
		if k, ok := m["Key"]; ok && k != nil {
			key, _ = k.(string)
		} else if k, ok := m["key"]; ok && k != nil {
			key, _ = k.(string)
		}

		// ดึงค่า value
		if v, ok := m["Value"]; ok {
			value = v
		} else if v, ok := m["value"]; ok {
			value = v
		}

		if key != "" {
			result[key] = value
			log.Printf("DEBUG: Added key-value pair: %s = %v", key, value)
		}
	}

	log.Printf("DEBUG: Successfully converted to map with %d entries", len(result))
	return result
}

func (h *Handler) DynamicAPIHandler(c *fiber.Ctx) error {
	key := c.Method() + ":" + c.Path()

	// 1. Find API Definition from Cache (Read Lock)
	h.routesMutex.RLock()
	api, exists := h.dynamicRoutes[key]
	h.routesMutex.RUnlock()

	if !exists {
		// ถ้าไม่เจอใน cache ลองหาใน DB อีกครั้งเผื่อกรี cache ไม่ sync?
		// หรือจะให้มี endpoint /reload APIs แทน? --> ใช้ /reload ดีกว่า
		// ถ้าต้องกาม robust สูง อาจจะ fallback ไปหาใน DB ตรงนี้
		// log.Printf("DEBUG: Route key '%s' not found in cache. Passing to next handler.", key)
		return c.Next() // Not found, pass to next handler (or 404 if this is the last)
	}

	log.Printf("INFO: Matched dynamic route for API '%s': %s %s", api.Name, api.Method, api.Endpoint)

	// 2. Prepare Request Data (รวม Query Params, Path Params, Body)
	reqData := make(map[string]interface{})

	// Path Params (มีความสำคัญสุด อาจะ overwrite ตัวอื่น)
	for k, v := range c.AllParams() {
		reqData[k] = v
	}

	// Query Params (รองลงมา)
	c.Request().URI().QueryArgs().VisitAll(func(k, v []byte) {
		keyStr := string(k)
		if _, exists := reqData[keyStr]; !exists { // ใส่ถ้ายังไม่มี key ซ้ำกับ Path Param
			reqData[keyStr] = string(v)
		}
	})

	// Body (ต่ำสุด ถ้าเป็น POST, PUT, PATCH)
	if c.Method() == fiber.MethodPost || c.Method() == fiber.MethodPut || c.Method() == fiber.MethodPatch {
		// ใช้ c.BodyRaw() เพื่ออ่าน body โดยไม่ consume แล้ว parse เอง หรือใช้ BodyParser ถ้าไม่ต้องการ raw body
		// การใช้ BodyParser จะสะดวกกว่าสำหรับการแปลงเป็น map[string]interface{}
		var bodyData map[string]interface{}
		if err := c.BodyParser(&bodyData); err == nil {
			for k, v := range bodyData {
				if _, exists := reqData[k]; !exists { // ใส่ถ้ายังไม่มี key ซ้ำกับ Path/Query Param
					reqData[k] = v
				}
			}
		} else if len(c.BodyRaw()) > 0 { // Log warning เฉพาะเมื่อมี body แต่ parse ไม่ได้
			log.Printf("WARN: Cannot parse request body for API '%s' (Method: %s): %v. Body params might be ignored.", api.Name, c.Method(), err)
		}
	}
	log.Printf("DEBUG: Request data for API '%s': %v", api.Name, reqData)

	// 3. Validate Required Parameters
	for _, param := range api.Parameters {
		if param.Required {
			val, paramExists := reqData[param.Name]
			// ตรวจสอบว่ามี key และค่าไม่เป็น nil หรือ string ว่าง (อาจจะต้องปรับตามความต้องการ)
			if !paramExists || val == nil || fmt.Sprintf("%v", val) == "" {
				log.Printf("WARN: Missing or empty required parameter '%s' for API '%s'", param.Name, api.Name)
				return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": "Missing or empty required parameter: " + param.Name})
			}
			// TODO: Add type validation based on param.Type
		}
	}

	// 4. Check Target Database/Collection
	if api.Database == "" || api.Collection == "" {
		log.Printf("ERROR: API definition '%s' is missing database or collection name", api.Name)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "API configuration error: missing target database or collection"})
	}

	// 5. Process Logic (Conditional Flow or Default)
	var response interface{}
	var dataForSaving map[string]interface{} // ข้อมูลที่จะใช้บันทึก (อาจะต่างจาก response)
	var saveData bool
	var processingError error
	ctx, cancel := context.WithTimeout(c.Context(), 20*time.Second) // Use Fiber context
	defer cancel()

	// --- สร้าง shallow copy ของ reqData เพื่อส่งให้ core logic ป้องกันการแก้ไข reqData โดยตรง ---
	currentDataState := make(map[string]interface{})
	for k, v := range reqData {
		currentDataState[k] = v
	}

	if api.ConditionalFlow != nil {
		// --- Use Conditional Flow ---
		log.Printf("DEBUG: Processing conditional flow for API '%s'", api.Name)
		// ProcessConditionalFlow ควรคืน:
		// 1. responseToSend: ข้อมูลที่จะส่งกลับให้กลอง client (อาจเป็น map, string, etc.)
		// 2. finalDataState: สถานะล่าสุดของข้อมูลหลังผ่าน transform (เป็น map[string]interface{} เสมอ)
		// 3. shouldSave: boolean บอกว่าควรบันทึก finalDataState หรือไม่
		// 4. err: error ที่เกิดขึ้นระหว่างประมวลผล
		responseToSend, finalDataState, shouldSave, err := core.ProcessConditionalFlow(api.ConditionalFlow, currentDataState, ctx, h.store, api.Database, api.Collection)
		if err != nil {
			log.Printf("ERROR: Failed to process conditional flow for API '%s': %v", api.Name, err)
			// TODO: Map specific error types from core to HTTP statuses
			processingError = fmt.Errorf("failed to process request logic: %w", err) // เก็บ error ไว้ก่อน
			response = fiber.Map{"error": processingError.Error()}                   // กำหนด response เป็น error message
			// พิจารณา status code ที่เหมาะสม
			c.Status(http.StatusInternalServerError) // ตั้ง status ไว้ก่อน อาจะถูก override ถ้า error เฉพาะเจาะจงกว่า
		} else {
			response = responseToSend
			saveData = shouldSave
			if saveData {
				dataForSaving = finalDataState // ใช้ finalDataState ในการบันทึก
			}
		}
		log.Printf("DEBUG: Conditional flow result for API '%s': saveData=%t, response=%v", api.Name, saveData, response)

	} else {
		// --- Use Default Logic ---
		log.Printf("DEBUG: No conditional flow defined for API '%s', using default logic.", api.Name)
		// Default logic ควรทำงานกับ currentDataState (ซึ่งเป็น copy ของ reqData)
		switch c.Method() {
		case fiber.MethodGet:
			filter := bson.M{}
			// ใช้ currentDataState (ที่มาจาก reqData) เป็น filter
			for k, v := range currentDataState {
				filter[k] = v
			}
			log.Printf("DEBUG: Default GET - Finding data in %s.%s with filter: %v", api.Database, api.Collection, filter)
			results, err := h.store.FindData(ctx, api.Database, api.Collection, filter) // Assuming FindData exists
			if err != nil {
				log.Printf("ERROR: Default GET - Failed to find data for API '%s': %v", api.Name, err)
				processingError = fmt.Errorf("failed to retrieve data: %w", err)
				response = fiber.Map{"error": processingError.Error()}
				c.Status(http.StatusInternalServerError)
			} else {
				response = results
				saveData = false // GET ไม่ควร save
			}

		case fiber.MethodPost, fiber.MethodPut:
			// Default: บันทึกข้อมูลที่เข้ามา (currentDataState)
			response = currentDataState // คืนข้อมูลที่รับมา (หรือที่จะบันทึก)
			saveData = true
			dataForSaving = currentDataState // ข้อมูลที่จะบันทึกคือข้อมูลที่เข้ามา
			log.Printf("DEBUG: Default POST/PUT - Data to be saved: %v", dataForSaving)

		case fiber.MethodDelete:
			filter := bson.M{}
			// ใช้ currentDataState เป็น filter
			for k, v := range currentDataState {
				filter[k] = v
			}
			if len(filter) == 0 {
				log.Printf("WARN: Default DELETE for API '%s' called without parameters to filter.", api.Name)
				processingError = errors.New("DELETE requires parameters to identify data to delete")
				response = fiber.Map{"error": processingError.Error()}
				c.Status(http.StatusBadRequest)
			} else {
				log.Printf("DEBUG: Default DELETE - Deleting data in %s.%s with filter: %v", api.Database, api.Collection, filter)
				delCount, err := h.store.DeleteData(ctx, api.Database, api.Collection, filter) // Assuming DeleteData returns count
				if err != nil {
					log.Printf("ERROR: Default DELETE - Failed to delete data for API '%s': %v", api.Name, err)
					processingError = fmt.Errorf("failed to delete data: %w", err)
					response = fiber.Map{"error": processingError.Error()}
					c.Status(http.StatusInternalServerError)
				} else {
					response = fiber.Map{"success": true, "deletedCount": delCount}
					saveData = false // DELETE ไม่ควร save (เว้นแต่จะมี logic แปลกๆ)
				}
			}

		default: // Handle other methods like PATCH, OPTIONS, HEAD if necessary
			response = fiber.Map{"success": true, "message": fmt.Sprintf("Method %s received", c.Method())}
			saveData = false
		}
	}

	// 6. Save Data if Required (and no prior processing error)
	if saveData && processingError == nil {
		if dataForSaving == nil {
			log.Printf("ERROR: SaveData is true for API '%s' but dataForSaving is nil. Skipping save.", api.Name)
			// อาจะตั้ง processingError หรือคืน Internal Server Error ที่นี่
			processingError = errors.New("internal error: data marked for saving is missing")
			response = fiber.Map{"error": processingError.Error()}
			c.Status(http.StatusInternalServerError)

		} else {
			log.Printf("DEBUG: Attempting to save data for API '%s' to %s.%s", api.Name, api.Database, api.Collection)
			saveCtx, saveCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer saveCancel()

			err := h.store.SaveData(saveCtx, api.Database, api.Collection, api.UniqueKey, dataForSaving)
			if err != nil {
				log.Printf("ERROR: Handler failed to save data for API '%s': %v", api.Name, err)
				processingError = fmt.Errorf("failed to save data to database: %w", err)
				// ั้ง response เป็น error ถ้ายังไม่มี error ก่อนหน้า
				if response == nil || (response.(fiber.Map)["error"] == nil) {
					response = fiber.Map{"error": processingError.Error()}
					c.Status(http.StatusInternalServerError)
				}
			} else {
				log.Printf("INFO: Data saved successfully for API '%s'", api.Name)
				// อาจะปรับ response เล็กน้อยเพื่อยืนยันว่า save สำเร็จ ถ้า response เดิมไม่มีข้อมูลนี้
				if respMap, ok := response.(fiber.Map); ok && respMap["message"] == nil && respMap["data"] == nil {
					respMap["message"] = "Data processed and saved successfully"
					response = respMap
				}
			}
		}
	} // End if saveData

	// 7. Return Final Response
	if processingError != nil {
		if c.Response().StatusCode() == http.StatusOK {
			c.Status(http.StatusInternalServerError)
		}
		if respMap, ok := response.(fiber.Map); !ok || respMap["error"] == nil {
			response = fiber.Map{"error": processingError.Error()}
		}
		log.Printf("DEBUG: Returning error response for API '%s': Status=%d, Body=%v", api.Name, c.Response().StatusCode(), response)
		return c.JSON(response)
	}

	// ถ้าไม่มี error และ response เป็น nil ให้ตั้งค่า default
	if response == nil {
		response = fiber.Map{"success": true}
	}

	// Set default status code
	// Set status code from response if available, otherwise use default
	statusCode := http.StatusOK
	if respMap, ok := response.(fiber.Map); ok {
		// Check for nested statusCode in opdResult
		if opdResult, exists := respMap["opdResult"].(map[string]interface{}); exists {
			if code, exists := opdResult["statusCode"]; exists {
				switch v := code.(type) {
				case int:
					statusCode = v
				case float64:
					statusCode = int(v)
				}
			}
		}
		// Check direct statusCode if not found in opdResult
		if code, exists := respMap["statusCode"]; exists {
			switch v := code.(type) {
			case int:
				statusCode = v
			case float64:
				statusCode = int(v)
			}
		}
	} else if primitiveDoc, ok := response.(primitive.D); ok {
		// Convert primitive.D to bson.M using Marshal/Unmarshal
		bytes, err := bson.Marshal(primitiveDoc)
		if err != nil {
			log.Printf("ERROR: Failed to marshal primitive.D: %v", err)
			statusCode = http.StatusInternalServerError
		} else {
			var convertedMap bson.M
			if err := bson.Unmarshal(bytes, &convertedMap); err != nil {
				log.Printf("ERROR: Failed to unmarshal to bson.M: %v", err)
				statusCode = http.StatusInternalServerError
			} else {
				if code, exists := convertedMap["statusCode"]; exists {
					switch v := code.(type) {
					case int32:
						statusCode = int(v)
					case int64:
						statusCode = int(v)
					case float64:
						statusCode = int(v)
					}
				}
			}
		}
	}

	c.Status(statusCode)

	// Ensure response is in fiber.Map format
	if _, ok := response.(fiber.Map); !ok {
		if mapResp, ok := response.(map[string]interface{}); ok {
			response = fiber.Map(mapResp)
		} else {
			// response = fiber.Map{
			// 	"data": response,
			// }
		}
	}

	// Convert array-style response to map if needed
	log.Printf("DEBUG: Response type before conversion: %T", response)

	// Special handling for MongoDB primitive types
	if primitiveDoc, ok := response.(primitive.D); ok {
		// Convert primitive.D to map[string]interface{} using Marshal/Unmarshal
		bytes, err := bson.Marshal(primitiveDoc)
		if err != nil {
			log.Printf("ERROR: Failed to marshal primitive.D: %v", err)
			response = fiber.Map{"error": "Internal server error"}
		} else {
			var convertedMap bson.M
			if err := bson.Unmarshal(bytes, &convertedMap); err != nil {
				log.Printf("ERROR: Failed to unmarshal to bson.M: %v", err)
				response = fiber.Map{"error": "Internal server error"}
			} else {
				response = convertedMap
				log.Printf("DEBUG: Converted primitive.D to standard response format")
			}
		}
	} else if respMap, ok := response.(fiber.Map); ok {
		// Handle nested data field
		if data, exists := respMap["data"]; exists {
			// Check if nested data is primitive.D
			if primitiveData, ok := data.(primitive.D); ok {
				// Convert nested primitive.D to map using Marshal/Unmarshal
				bytes, err := bson.Marshal(primitiveData)
				if err != nil {
					log.Printf("ERROR: Failed to marshal nested primitive.D: %v", err)
				} else {
					var convertedData bson.M
					if err := bson.Unmarshal(bytes, &convertedData); err != nil {
						log.Printf("ERROR: Failed to unmarshal nested data to bson.M: %v", err)
					} else {
						respMap["data"] = convertedData
					}
				}
			} else {
				converted := convertArrayToMap(data)
				respMap["data"] = converted
			}
			response = respMap["data"]
			log.Printf("DEBUG: Converted nested data field in fiber.Map")
		}
	} else {
		// ตรวจสอบว่า response เป็น array หรือไม่
		isArray := false

		// ตรวจสอบหลายรูปแบบของ array
		if _, ok := response.([]interface{}); ok {
			isArray = true
			log.Printf("DEBUG: Response is []interface{}")
		} else if _, ok := response.([]map[string]interface{}); ok {
			isArray = true
			log.Printf("DEBUG: Response is []map[string]interface{}")
		} else {
			// ตรวจสอบด้วย reflection
			responseType := fmt.Sprintf("%T", response)
			if strings.HasPrefix(responseType, "[]") {
				isArray = true
				log.Printf("DEBUG: Response is array type: %s", responseType)
			}
		}

		if isArray {
			// แปลง array เป็น map
			converted := convertArrayToMap(response)
			log.Printf("DEBUG: Array converted to: %T %v", converted, converted)

			// ตรวจสอบว่าการแปลงสำเร็จหรือไม่
			if convertedMap, ok := converted.(map[string]interface{}); ok && len(convertedMap) > 0 {
				response = convertedMap
				log.Printf("DEBUG: Successfully wrapped converted map in standard response")
			} else {
				log.Printf("DEBUG: Wrapped original array in standard response")
			}
		}
	}

	// Ensure consistent response format
	if finalResp, ok := response.(fiber.Map); ok {
		if _, hasStatus := finalResp["status"]; !hasStatus {
			response = finalResp
		}
	}

	return c.JSON(response)
}

// --- Helper Functions (อาจะมี ถ้าจำเป็น) ---

// ตัวอย่าง ReloadAPIs (ต้องเพิ่มใน Handler และ Routes)
/*
func (h *Handler) ReloadAPIs(c *fiber.Ctx) error {
	log.Println("INFO: Received request to reload APIs...")
	loadCtx, loadCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer loadCancel()

	newAPIs, err := h.store.LoadAPIs(loadCtx)
	if err != nil {
		log.Printf("ERROR: Failed to reload APIs from database: %v", err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to reload APIs"})
	}

	h.routesMutex.Lock()
	h.dynamicRoutes = newAPIs // Replace the entire map
	h.routesMutex.Unlock()

	count := len(newAPIs)
	log.Printf("INFO: Successfully reloaded %d APIs into cache.", count)
	return c.Status(http.StatusOK).JSON(fiber.Map{
		"message":    "APIs reloaded successfully",
		"loadedCount": count,
	})
}
*/
