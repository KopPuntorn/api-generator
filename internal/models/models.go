package models

import (
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Condition defines a single condition for evaluation.
type Condition struct {
	Field      string      `json:"field" bson:"field"`                               // Field name in the data to check
	Operator   string      `json:"operator" bson:"operator"`                         // Comparison operator (e.g., "eq", "gt", "contains")
	Value      interface{} `json:"value" bson:"value"`                               // Value to compare against
	Action     string      `json:"action,omitempty" bson:"action,omitempty"`         // (Optional) Legacy or specific use?
	ReturnData interface{} `json:"returnData,omitempty" bson:"returnData,omitempty"` // (Optional) Legacy or specific use?
}

// ConditionalBlock defines a block with conditions and subsequent actions.
type ConditionalBlock struct {
	Conditions []Condition       `json:"conditions" bson:"conditions"`         // Conditions to evaluate (AND logic)
	Then       *ActionDefinition `json:"then,omitempty" bson:"then,omitempty"` // Action if conditions are true
	Else       *ActionDefinition `json:"else,omitempty" bson:"else,omitempty"` // Action if conditions are false
}

// ActionDefinition defines an action to perform after condition evaluation.
type ActionDefinition struct {
	Type            string            `json:"type" bson:"type"`                                           // Action type: "return", "continue", "conditionalBlock", "apiCall"
	ReturnData      interface{}       `json:"returnData,omitempty" bson:"returnData,omitempty"`           // Data to return if type is "return"
	ConditionalFlow *ConditionalBlock `json:"conditionalFlow,omitempty" bson:"conditionalFlow,omitempty"` // Next block if type is "conditionalBlock"
	SaveData        bool              `json:"saveData" bson:"saveData"`                                   // Flag indicating if data should be saved
	Transform       []Transformation  `json:"transform,omitempty" bson:"transform,omitempty"`             // Data transformations to apply
	ApiCall         *ApiCall          `json:"apiCall,omitempty" bson:"apiCall,omitempty"`                // API call configuration if type is "apiCall"
}

// Transformation defines a data transformation operation.
type Transformation struct {
	Operation string      `json:"operation" bson:"operation"`                 // Operation: "set", "remove", "append", "calculate"
	Field     string      `json:"field" bson:"field"`                         // Target field for the operation
	Value     interface{} `json:"value,omitempty" bson:"value,omitempty"`     // Value for "set", "append"
	Formula   string      `json:"formula,omitempty" bson:"formula,omitempty"` // Formula for "calculate" (e.g., "add:field1,field2")
}

// ApiDefinition holds the metadata and logic for a dynamic API endpoint.
type ApiDefinition struct {
	ID              primitive.ObjectID     `json:"id,omitempty" bson:"_id,omitempty"`
	Name            string                 `json:"name" bson:"name"`                                           // Unique name for the API definition
	Endpoint        string                 `json:"endpoint" bson:"endpoint"`                                   // HTTP path (e.g., "/users/:id")
	Method          string                 `json:"method" bson:"method"`                                       // HTTP method (e.g., "GET", "POST")
	Database        string                 `json:"database" bson:"database"`                                   // Target database name for data operations
	Collection      string                 `json:"collection" bson:"collection"`                               // Target collection name for data operations
	Parameters      []Parameter            `json:"parameters,omitempty" bson:"parameters,omitempty"`           // Definition of expected parameters
	ResponseSchema  map[string]interface{} `json:"responseSchema,omitempty" bson:"responseSchema,omitempty"`   // (Optional) Schema for validating response
	ConditionalFlow *ConditionalBlock      `json:"conditionalFlow,omitempty" bson:"conditionalFlow,omitempty"` // Root conditional logic block
	CreatedAt       time.Time              `json:"createdAt" bson:"createdAt"`                                 // Timestamp of creation
	UniqueKey       string                 `json:"uniqueKey,omitempty" bson:"uniqueKey,omitempty"`             // Field name used as the unique key for Upsert operations
}

// Parameter defines an expected parameter for an API endpoint.
type Parameter struct {
	Name     string `json:"name" bson:"name"`         // Parameter name
	Type     string `json:"type" bson:"type"`         // Expected data type (e.g., "string", "number", "boolean") for validation
	Required bool   `json:"required" bson:"required"` // Whether the parameter is mandatory
}

// Represents an error type for "Not Found" scenarios in the database layer.
type ErrNotFound struct {
	Resource string
	Query    string
}

type ApiCall struct {
	ApiName     string                 `json:"apiName" bson:"apiName"`         // Name of the target API to call
	Parameters  map[string]interface{} `json:"parameters" bson:"parameters"`   // Parameters to pass to the target API
	ResultField string                 `json:"resultField" bson:"resultField"` // Field to store the API call result
}

func (e *ErrNotFound) Error() string {
	return fmt.Sprintf("%s not found for query: %s", e.Resource, e.Query)
}

// Represents an error type for validation issues.
type ErrValidation struct {
	Message string
}

func (e *ErrValidation) Error() string {
	return e.Message
}

// Represents an error type for duplicate entries.
type ErrDuplicate struct {
	Message string
}

func (e *ErrDuplicate) Error() string {
	return e.Message
}

// Remove CallApi field from ApiDefinition struct
