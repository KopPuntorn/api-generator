package core

import (
	"context"
	// "net/http"
	// "errors"
	"fmt"
	"log"
	"reflect"
	"strconv" // ใช้สำหรับแปลง string เป็น float
	"strings"

	// --- เปลี่ยน your_module_name เป็นชื่อ Module Go ของคุณ ---
	"api-genarator/internal/database"
	"api-genarator/internal/models"

	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	// --- ---------------------------------------------------
	// "go.mongodb.org/mongo-driver/mongo" // อาจจะไม่จำเป็น ถ้า Action ไม่เรียก DB โดยตรง
)

// ProcessConditionalFlow is the main entry point for processing a conditional block.
// It evaluates conditions and executes the 'Then' or 'Else' action.
// It returns:
// - responseToSend: The data intended to be sent back to the API client.
// - finalDataState: The final state of the data map after processing this block and its actions (used for saving).
// - shouldSave: A boolean indicating if the finalDataState should be saved by the caller.
// - err: Any error encountered during processing.
func ProcessConditionalFlow(flow *models.ConditionalBlock,
	initialData map[string]interface{},
	ctx context.Context,
	store *database.Store, // Pass store for potential future db operations within actions
	dbName, collName string) (responseToSend interface{}, finalDataState map[string]interface{}, shouldSave bool, err error) {

	log.Printf("DEBUG: Processing Conditional Flow...")

	// Start with the initial data state
	currentDataState := initialData
	// Default return values
	responseToSend = currentDataState // Default response is the current data
	finalDataState = currentDataState // Default final state is the current data
	shouldSave = false                // Default is not to save

	if flow == nil {
		log.Printf("DEBUG: Conditional flow is nil, returning initial data state.")
		return initialData, initialData, false, nil // Return initial state, don't save
	}

	// Evaluate the conditions for the current block
	conditionsMet := evaluateConditions(flow.Conditions, currentDataState)

	var actionToProcess *models.ActionDefinition
	if conditionsMet {
		log.Printf("DEBUG: Conditions MET. Processing 'Then' action.")
		actionToProcess = flow.Then
	} else {
		log.Printf("DEBUG: Conditions NOT MET. Processing 'Else' action.")
		actionToProcess = flow.Else
	}

	// If there's an action to process (either Then or Else)
	if actionToProcess != nil {
		// Process the chosen action
		responseFromAction, dataAfterAction, saveFromAction, actionErr := processAction(actionToProcess, currentDataState, ctx, store, dbName, collName)
		if actionErr != nil {
			log.Printf("ERROR: Error processing action: %v", actionErr)
			// Return the error, potentially setting a default error response
			return fiber.Map{"error": actionErr.Error()}, dataAfterAction, false, actionErr // Return error response, last known data state, don't save
		}
		// Update return values based on the action's result
		responseToSend = responseFromAction
		finalDataState = dataAfterAction
		shouldSave = saveFromAction
	} else {
		// No 'Then' or 'Else' action defined for the matched condition state
		log.Printf("DEBUG: No action defined for the current condition outcome. Returning current data state.")
		// Return the data state as it was before checking Then/Else, don't save
		return currentDataState, currentDataState, false, nil
	}

	return responseToSend, finalDataState, shouldSave, nil
}

// evaluateConditions checks if all conditions in a slice are met (AND logic).
func evaluateConditions(conditions []models.Condition, data map[string]interface{}) bool {
	if len(conditions) == 0 {
		log.Printf("DEBUG: evaluateConditions - No conditions provided, returning true.")
		return true // No conditions means the block is always entered (or skipped if used differently)
	}
	log.Printf("DEBUG: Evaluating %d conditions...", len(conditions))
	for i, cond := range conditions {
		met := evaluateCondition(cond, data)
		log.Printf("DEBUG: Condition #%d (%s %s %v) evaluated to: %t", i+1, cond.Field, cond.Operator, cond.Value, met)
		if !met {
			return false // If any condition is false, the whole block is false (AND logic)
		}
	}
	log.Printf("DEBUG: All %d conditions evaluated to true.", len(conditions))
	return true // All conditions were true
}

// evaluateCondition checks a single condition against the data.
func evaluateCondition(condition models.Condition, data map[string]interface{}) bool {
	// Support nested field access (e.g., "opdResult.statusCode")
	fieldParts := strings.Split(condition.Field, ".")
	fieldValue := interface{}(data)

	for _, part := range fieldParts {
		if m, ok := fieldValue.(map[string]interface{}); ok {
			fieldValue = m[part]
		} else {
			log.Printf("DEBUG: Cannot access nested field '%s' in path '%s'", part, condition.Field)
			return false
		}
	}

	// Continue with existing field value handling...
	fieldValue, exists := data[condition.Field]

	// How to handle non-existent fields depends on the operator
	if !exists {
		// If field doesn't exist:
		// - 'neq' (not equal) should be true (it's definitely not equal to the value)
		// - 'eq' (equal) should be false (it's not equal to the value)
		// - Other comparisons like gt, lt, contains, in are generally false.
		log.Printf("DEBUG: Field '%s' does not exist in data.", condition.Field)
		return condition.Operator == "neq"
	}

	// Handle nil field value explicitly for some operators
	if fieldValue == nil {
		// If field value is nil:
		// - 'eq' is true only if condition.Value is also nil.
		// - 'neq' is true only if condition.Value is not nil.
		// - Other comparisons are generally false.
		switch condition.Operator {
		case "eq":
			return condition.Value == nil
		case "neq":
			return condition.Value != nil
		default:
			log.Printf("DEBUG: Field '%s' is nil, operator '%s' evaluates to false.", condition.Field, condition.Operator)
			return false
		}
	}

	// Proceed with operator logic for non-nil field values
	switch condition.Operator {
	case "eq":
		// Use DeepEqual for robust comparison of potentially complex types (slices, maps)
		// Note: Be mindful of numeric type differences (e.g., int(1) vs float64(1.0)). DeepEqual treats them as different.
		// Consider converting to a common type (like string or float64) before comparing if necessary.
		return reflect.DeepEqual(fieldValue, condition.Value)

	case "neq":
		return !reflect.DeepEqual(fieldValue, condition.Value)

	case "contains": // Primarily for strings, could be extended for slices/maps
		sVal, ok1 := fieldValue.(string)
		cVal, ok2 := condition.Value.(string)
		if ok1 && ok2 {
			return strings.Contains(sVal, cVal)
		}
		log.Printf("WARN: 'contains' operator currently expects string field and value. Got field type %T, value type %T. Evaluating as false.", fieldValue, condition.Value)
		return false

	case "in": // Checks if fieldValue exists within condition.Value (which should be a slice/array)
		valSliceValue := reflect.ValueOf(condition.Value)
		if valSliceValue.Kind() != reflect.Slice && valSliceValue.Kind() != reflect.Array {
			log.Printf("WARN: 'in' operator requires an array/slice for condition value. Got type %T. Evaluating as false.", condition.Value)
			return false
		}
		for i := 0; i < valSliceValue.Len(); i++ {
			item := valSliceValue.Index(i).Interface()
			// Use DeepEqual to compare the field value with each item in the slice
			if reflect.DeepEqual(fieldValue, item) {
				return true // Found a match
			}
		}
		return false // No match found

	// Numeric Comparisons (gt, lt, gte, lte)
	case "gt", "lt", "gte", "lte":
		fvFloat, okFv := convertToFloat64(fieldValue)
		cvFloat, okCv := convertToFloat64(condition.Value)

		if !okFv || !okCv {
			log.Printf("WARN: Operator '%s' requires comparable numeric field and value. Could not convert field ('%v' type %T) or value ('%v' type %T) to float64. Evaluating as false.",
				condition.Operator, fieldValue, fieldValue, condition.Value, condition.Value)
			return false
		}

		switch condition.Operator {
		case "gt":
			return fvFloat > cvFloat
		case "lt":
			return fvFloat < cvFloat
		case "gte":
			return fvFloat >= cvFloat
		case "lte":
			return fvFloat <= cvFloat
		}

	default:
		log.Printf("WARN: Unknown operator '%s' encountered in condition. Evaluating as false.", condition.Operator)
		return false
	}
	// Should not be reached
	return false
}

// processAction handles the execution of a specific action (return, continue, conditionalBlock).
// It first applies transformations, then executes the action logic.
// It returns:
// - responseToSend: The data determined by the action (e.g., return data, data to continue with).
// - dataAfterAction: The state of the data map *after* transformations and action execution.
// - shouldSave: The boolean save flag from the action definition.
// - err: Any error encountered.
func processAction(action *models.ActionDefinition,
	dataBeforeAction map[string]interface{},
	ctx context.Context,
	store *database.Store,
	dbName, collName string) (responseToSend interface{}, dataAfterAction map[string]interface{}, shouldSave bool, err error) {

	if action == nil {
		log.Printf("WARN: processAction called with nil action.")
		// Return the data as it was, don't save
		return dataBeforeAction, dataBeforeAction, false, nil
	}

	log.Printf("DEBUG: Processing Action: Type=%s, SaveData=%t", action.Type, action.SaveData)

	// --- 1. Apply Transformations ---
	// Transformations modify the data state *before* the action type logic is executed.
	// ApplyTransformations returns a *new* map, preserving the original dataBeforeAction if needed.
	dataAfterTransform := ApplyTransformations(action.Transform, dataBeforeAction) // Calls func in transform.go
	log.Printf("DEBUG: Data state after transformations: %v", dataAfterTransform)

	// Initialize return values based on the state after transformation
	responseToSend = dataAfterTransform  // Default response is the transformed data
	dataAfterAction = dataAfterTransform // Current final state is the transformed data
	shouldSave = action.SaveData         // Inherit SaveData flag from action definition

	// --- 2. Execute Action Logic ---
	switch action.Type {
	case "return":
		// Substitute variables in the defined ReturnData using the state *after* transformations
		var finalReturnData interface{}

		// Handle both array and object return data formats
		switch v := action.ReturnData.(type) {
		case []interface{}: // ถ้าเป็น Array
			// Convert array of key-value pairs to map
			returnMap := make(map[string]interface{})
			for _, item := range v {
				if kvPair, ok := item.(map[string]interface{}); ok {
					if key, hasKey := kvPair["Key"].(string); hasKey {
						returnMap[key] = kvPair["Value"]
					}
				}
				finalReturnData = SubstituteVariables(returnMap, dataAfterTransform)
			}
		default: // ถ้าเป็น Object ปกติ
			finalReturnData = SubstituteVariables(action.ReturnData, dataAfterTransform)
		}

		log.Printf("DEBUG: Action 'return'. Returning data: %v", finalReturnData)
		responseToSend = finalReturnData // Set the specific response
		// dataAfterAction remains dataAfterTransform
		// shouldSave remains action.SaveData
		return responseToSend, dataAfterAction, shouldSave, nil

	case "conditionalBlock":
		if action.ConditionalFlow == nil {
			log.Printf("WARN: Action type is 'conditionalBlock' but ConditionalFlow is nil.")
			// Treat as 'continue'? Return current state.
			return dataAfterTransform, dataAfterTransform, action.SaveData, nil
		}
		log.Printf("DEBUG: Action 'conditionalBlock'. Processing nested flow...")
		// Recursively call ProcessConditionalFlow with the *transformed* data state
		// The results of the nested flow become the results of this action
		return ProcessConditionalFlow(action.ConditionalFlow, dataAfterTransform, ctx, store, dbName, collName)

	case "continue":
		log.Printf("DEBUG: Action 'continue'. Proceeding with current data state.")
		// Return the transformed data state as both response and final state
		// shouldSave remains action.SaveData
		return dataAfterTransform, dataAfterTransform, action.SaveData, nil

	case "apiCall":
		if action.ApiCall == nil {
			log.Printf("WARN: Action type is 'apiCall' but ApiCall configuration is nil")
			return fiber.Map{
				"status":  "error",
				"message": "Invalid API call configuration",
			}, dataAfterTransform, false, nil
		}

		// Get the target API definition
		targetAPI, err := store.GetAPIDefinitionByName(ctx, action.ApiCall.ApiName)
		if err != nil {
			log.Printf("ERROR: Failed to get target API '%s': %v", action.ApiCall.ApiName, err)
			return fiber.Map{"error": fmt.Sprintf("Failed to process API call to %s", action.ApiCall.ApiName)},
				dataAfterTransform, false, err
		}

		// Prepare parameters for the target API
		callParams := make(map[string]interface{})
		for k, v := range action.ApiCall.Parameters {
			// Handle variable references (e.g., $userId, $user.profile.id)
			if strVal, ok := v.(string); ok && strings.HasPrefix(strVal, "$") {
				paramName := strings.TrimPrefix(strVal, "$")
				fieldParts := strings.Split(paramName, ".")

				// Traverse nested structure
				value := interface{}(dataAfterTransform)
				for _, part := range fieldParts {
					if m, ok := value.(map[string]interface{}); ok {
						if val, exists := m[part]; exists {
							value = val
						} else {
							log.Printf("WARN: Nested field part '%s' not found in path '%s'", part, paramName)
							value = nil
							break
						}
					} else {
						log.Printf("WARN: Cannot traverse nested field '%s' in path '%s'", part, paramName)
						value = nil
						break
					}
				}

				if value != nil {
					callParams[k] = value
				}
			} else {
				callParams[k] = v
			}
		}

		// Validate required parameters
		for k, v := range callParams {
			if v == nil {
				log.Printf("WARN: Required parameter '%s' is nil", k)
				return fiber.Map{
					"status":  "error",
					"message": fmt.Sprintf("Missing required parameter: %s", k),
				}, dataAfterTransform, false, nil
			}
		}

		// Process the target API using its conditional flow
		apiResponse, _, _, callErr := ProcessConditionalFlow(
			targetAPI.ConditionalFlow,
			callParams,
			ctx,
			store,
			targetAPI.Database,
			targetAPI.Collection,
		)
		if callErr != nil {
			log.Printf("ERROR: Failed to process API call to '%s': %v", action.ApiCall.ApiName, callErr)
			return fiber.Map{"error": fmt.Sprintf("API call to %s failed: %v", action.ApiCall.ApiName, callErr)},
				dataAfterTransform, false, callErr
		}

		// Extract the actual response data we want
		var processedResponse interface{}
		switch v := apiResponse.(type) {
		case primitive.D:
			// Convert primitive.D to bson bytes then to map using Marshal/Unmarshal
			data, err := bson.Marshal(v)
			if err != nil {
				log.Printf("ERROR: Failed to marshal primitive.D: %v", err)
				processedResponse = v
			} else {
				var m bson.M
				if err := bson.Unmarshal(data, &m); err != nil {
					log.Printf("ERROR: Failed to unmarshal to bson.M: %v", err)
					processedResponse = v
				} else {
					processedResponse = m
				}
			}
		case fiber.Map:
			if data, ok := v["data"]; ok {
				processedResponse = data
			} else {
				processedResponse = v
			}
		default:
			processedResponse = v
		}

		// Store the result and continue processing
		// Store the result in potentially nested structure
		resultField := action.ApiCall.ResultField
		if strings.Contains(resultField, ".") {
			parts := strings.Split(resultField, ".")
			current := dataAfterTransform

			// Create nested structure if needed
			for i := 0; i < len(parts)-1; i++ {
				if _, exists := current[parts[i]]; !exists {
					current[parts[i]] = make(map[string]interface{})
				}
				if next, ok := current[parts[i]].(map[string]interface{}); ok {
					current = next
				} else {
					log.Printf("WARN: Cannot create nested structure at '%s'", strings.Join(parts[:i+1], "."))
					return fiber.Map{
						"status":  "error",
						"message": "Invalid result field path",
					}, dataAfterTransform, false, nil
				}
			}

			// Store the result in the final nested location
			current[parts[len(parts)-1]] = processedResponse
		} else {
			dataAfterTransform[resultField] = processedResponse
		}

		// Create a new map for final state
		finalState := make(map[string]interface{})
		for k, v := range dataAfterTransform {
			finalState[k] = v
		}

		// Apply transformations AFTER storing API call result
		finalState = ApplyTransformations(action.Transform, finalState)

		// Apply variable substitution on the final state
		if returnMap, ok := action.ReturnData.(map[string]interface{}); ok {
			finalReturnData := SubstituteVariables(returnMap, finalState)
			if finalResult, ok := finalReturnData.(map[string]interface{}); ok {
				return finalResult, finalResult, action.SaveData, nil
			}
		}

		// If type assertions fail, return the transformed state directly
		return finalState, finalState, action.SaveData, nil

	default:
		log.Printf("ERROR: Unknown action type '%s' in action definition.", action.Type)
		err = fmt.Errorf("unknown action type: %s", action.Type)
		return fiber.Map{"error": err.Error()}, dataAfterTransform, false, err
	}
}

// convertToFloat64 attempts to convert various numeric types (and strings) to float64.
func convertToFloat64(val interface{}) (float64, bool) {
	if val == nil {
		return 0, false
	}
	switch v := val.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		// Be cautious about potential precision loss for very large uint64
		return float64(v), true
	case string:
		// Try to parse string as float
		f, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return f, true
		}
		// Maybe try parsing as int first? Depends on desired behavior.
	case bool:
		// Convert bool to 0 or 1?
		if v {
			return 1.0, true
		}
		return 0.0, true
		// Add other types if necessary (e.g., time.Time converted to Unix timestamp)
	}

	// If direct type assertion/conversion fails, try reflection as a last resort (less efficient)
	rv := reflect.ValueOf(val)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	}

	log.Printf("TRACE: Could not convert type %T (%v) to float64", val, val)
	return 0, false
}

// --- Placeholder for potential future DB Operation action ---
/*
func executeDbOperation(action *models.ActionDefinition, data map[string]interface{}, ctx context.Context, store *database.Store, defaultDbName, defaultCollName string) (interface{}, error) {
	// 1. Determine target DB and Collection (from action or default)
	dbName := defaultDbName
	collName := defaultCollName
	if action.TargetDatabase != "" { dbName = action.TargetDatabase } // Assuming these fields exist in ActionDefinition
	if action.TargetCollection != "" { collName = action.TargetCollection }

	// 2. Get collection handle
	collection, err := store.GetClient().Database(dbName).Collection(collName) // Or use store helper if available
	if err != nil {
		return nil, fmt.Errorf("failed to get collection %s.%s: %w", dbName, collName, err)
	}

	// 3. Substitute variables in filter/update data defined in action
	filterDataRaw := SubstituteVariables(action.Filter, data)   // Assuming ActionDefinition has Filter field
	updateDataRaw := SubstituteVariables(action.UpdateData, data) // Assuming ActionDefinition has UpdateData field

    // Convert filter/update data to bson.M or appropriate type
    filter, ok := filterDataRaw.(map[string]interface{})
    if !ok && filterDataRaw != nil { return nil, errors.New("substituted filter is not a valid map") }
    update, ok := updateDataRaw.(map[string]interface{})
     if !ok && updateDataRaw != nil { return nil, errors.New("substituted update data is not a valid map") }


	// 4. Perform the operation based on action.Operation
	switch action.Operation { // Assuming ActionDefinition has Operation field
	case "findOne":
		var result bson.M
		err := collection.FindOne(ctx, filter).Decode(&result)
		if err != nil {
            if errors.Is(err, mongo.ErrNoDocuments) { return nil, database.ErrNotFound }
			return nil, fmt.Errorf("findOne failed: %w", err)
		}
		return result, nil
	case "updateOne":
        if len(update) == 0 { return nil, errors.New("update data is empty") }
		updateDoc := bson.M{"$set": update} // Or use raw update if more complex ($inc, etc.)
		result, err := collection.UpdateOne(ctx, filter, updateDoc, options.Update()) // Add upsert?
		if err != nil {
			return nil, fmt.Errorf("updateOne failed: %w", err)
		}
		return result, nil // Return mongo update result
    // Add find, deleteOne, deleteMany, insertOne, etc.
	default:
		return nil, fmt.Errorf("unsupported dbOperation: %s", action.Operation)
	}
}
*/
