package core

import (
	"fmt"
	"log"
	"strings"
	// "strconv" // อาจจะจำเป็นถ้า calculate มีการแปลง type ซับซ้อน

	// --- เปลี่ยน your_module_name เป็นชื่อ Module Go ของคุณ ---
	"api-genarator/internal/models"
	// --- ---------------------------------------------------
)

// ApplyTransformations applies a series of transformations to a data map.
// It returns a *new* map with the transformations applied, leaving the original map unchanged.
func ApplyTransformations(transformations []models.Transformation, data map[string]interface{}) map[string]interface{} {
	if len(transformations) == 0 {
		return data // ถ้าไม่มี transform ก็คืน map เดิมไปเลย (ไม่ต้อง copy)
	}

	log.Printf("DEBUG: Applying %d transformations...", len(transformations))

	// *** สำคัญ: สร้าง shallow copy ของ map เพื่อไม่แก้ไข map เดิมโดยตรง ***
	// การเปลี่ยนแปลงควรเกิดขึ้นเฉพาะใน scope ของ action นี้
	result := make(map[string]interface{})
	for k, v := range data {
		result[k] = v
	}

	// วน loop กลายการ transformations บน map ที่ copy มา
	for _, t := range transformations {
		log.Printf("DEBUG: Applying transformation: Op=%s, Field=%s, Value=%v, Formula=%s", t.Operation, t.Field, t.Value, t.Formula)
		switch t.Operation {
		case "set":
			// Handle variable substitution for set operation
			if strVal, ok := t.Value.(string); ok && strings.HasPrefix(strVal, "$") {
				if substituted := SubstituteVariables(t.Value, result); substituted != nil {
					result[t.Field] = substituted
				}
			} else {
				result[t.Field] = t.Value
			}

			// Set หรือ Replace ค่าใน field ที่ระบุ
			// อาจะต้องทำ variable substitution สำหรับ t.Value ด้วย ถ้าต้องการให้ value มาจาก field อื่นได้
			// result[t.Field] = SubstituteVariables(t.Value, result) // <-- ถ้าต้องการแทนที่ค่า value ด้วย

		case "remove":
			// ลบ field ออกจาก map
			delete(result, t.Field)

		case "append": // ต่อ string หรืออาจจะเพิ่ม item ใน slice? (ตอนนี้เน้น string)
			currentVal, exists := result[t.Field]
			valueToAppend := SubstituteVariables(t.Value, result)
			// valueToAppend = SubstituteVariables(t.Value, result) // <-- ถ้าต้องการแทนที่ค่า value ด้วย

			if !exists || currentVal == nil {
				// ถ้า field เดิมไม่มีอยู่ หรือเป็น nil ก็ set ค่าใหม่ไปเลย
				result[t.Field] = valueToAppend
			} else {
				// ถ้า field เดิมมีอยู่ พยายามต่อ string
				// ใช้ fmt.Sprintf เพื่อรองรับการต่อค่าที่ไม่ใช่ string ได้ดีขึ้น
				result[t.Field] = fmt.Sprintf("%v%v", currentVal, valueToAppend)
				/* // Logic เดิมที่เน้น string:
				currentStr, ok1 := currentVal.(string)
				appendStr, ok2 := valueToAppend.(string)
				if ok1 && ok2 {
					result[t.Field] = currentStr + appendStr
				} else {
					log.Printf("WARN: 'append' operation works best with strings. Field '%s' type: %T, Value type: %T. Converting using fmt.Sprintf.", t.Field, currentVal, valueToAppend)
					result[t.Field] = fmt.Sprintf("%v%v", currentVal, valueToAppend)
				}
				*/
			}

		case "calculate": // คำนวณตามสูตร
			if t.Formula == "" || t.Field == "" {
				log.Printf("WARN: 'calculate' operation requires 'field' and 'formula'. Skipping.")
				continue
			}
			// *** Implement การ parse formula และคำนวณ ***
			// ตัวอย่าง: formula = "add:field1,$field2,-field3,10.5"
			// ตัวอย่าง: formula = "multiply:price,quantity"
			parts := strings.SplitN(t.Formula, ":", 2)
			if len(parts) != 2 {
				log.Printf("WARN: Invalid formula format for 'calculate' operation: '%s'. Expected 'operation:field1,field2,...'. Skipping.", t.Formula)
				continue
			}
			calcOp := strings.ToLower(strings.TrimSpace(parts[0]))
			fieldArgs := strings.Split(parts[1], ",")

			var calcResult float64
			calculationPossible := true // Flag ว่าการคำนวณทำได้หรือไม่

			switch calcOp {
			case "add", "sum":
				calcResult = 0.0
				for _, arg := range fieldArgs {
					arg = strings.TrimSpace(arg)
					if arg == "" {
						continue
					}

					isSubtract := strings.HasPrefix(arg, "-")
					if isSubtract {
						arg = strings.TrimPrefix(arg, "-")
					}

					// ลองดึงค่าจาก data หรือเป็น literal number
					numVal, ok := getValueAsFloat(arg, result)
					if !ok {
						log.Printf("WARN: Could not get numeric value for '%s' in formula '%s'. Skipping this argument.", arg, t.Formula)
						// อาจะทำให้การคำนวณนี้ไม่สำเร็จไปเลย? หรือแค่ข้าม arg นี้? -> ข้าม arg
						continue // ข้าม argument นี้
					}

					if isSubtract {
						calcResult -= numVal
					} else {
						calcResult += numVal
					}
				}
				// ถ้าไม่มีตัวเลขที่ถูกต้องเลย calcResult จะเป็น 0

			case "multiply", "product":
				calcResult = 1.0
				hasValidOperand := false
				for _, arg := range fieldArgs {
					arg = strings.TrimSpace(arg)
					if arg == "" {
						continue
					}
					// การคูณไม่มีการลบนำหน้า

					numVal, ok := getValueAsFloat(arg, result)
					if !ok {
						log.Printf("WARN: Could not get numeric value for '%s' in formula '%s'. Skipping this argument.", arg, t.Formula)
						continue // ข้าม argument นี้
					}
					calcResult *= numVal
					hasValidOperand = true
				}
				// ถ้าไม่มี operand ที่ถูกต้องเลย อาจะให้ผลเป็น 0 หรือ 1? -> ให้เป็น 0 ตาม logic เดิม
				if !hasValidOperand {
					calcResult = 0.0
				}

			// TODO: เพิ่ม operation อื่นๆ เช่น divide, average, subtract (แยก)
			case "subtract": // ตัวอย่าง: subtract:minuend,subtrahend1,subtrahend2...
				if len(fieldArgs) < 2 {
					log.Printf("WARN: 'subtract' requires at least two arguments (minuend, subtrahend). Formula: '%s'", t.Formula)
					calculationPossible = false
					break
				}
				minuendArg := strings.TrimSpace(fieldArgs[0])
				minuend, ok := getValueAsFloat(minuendArg, result)
				if !ok {
					log.Printf("WARN: Could not get numeric value for minuend '%s' in formula '%s'", minuendArg, t.Formula)
					calculationPossible = false
					break
				}
				calcResult = minuend
				for i := 1; i < len(fieldArgs); i++ {
					subtrahendArg := strings.TrimSpace(fieldArgs[i])
					subtrahend, ok := getValueAsFloat(subtrahendArg, result)
					if !ok {
						log.Printf("WARN: Could not get numeric value for subtrahend '%s' in formula '%s'. Skipping argument.", subtrahendArg, t.Formula)
						continue
					}
					calcResult -= subtrahend
				}

			case "divide": // ตัวอย่าง: divide:dividend,divisor
				if len(fieldArgs) != 2 {
					log.Printf("WARN: 'divide' requires exactly two arguments (dividend, divisor). Formula: '%s'", t.Formula)
					calculationPossible = false
					break
				}
				dividendArg := strings.TrimSpace(fieldArgs[0])
				divisorArg := strings.TrimSpace(fieldArgs[1])
				dividend, ok1 := getValueAsFloat(dividendArg, result)
				divisor, ok2 := getValueAsFloat(divisorArg, result)

				if !ok1 || !ok2 {
					log.Printf("WARN: Could not get numeric values for dividend or divisor in formula '%s'", t.Formula)
					calculationPossible = false
					break
				}
				if divisor == 0 {
					log.Printf("WARN: Division by zero attempted in formula '%s'. Setting result to 0.", t.Formula)
					calcResult = 0 // หรือจะให้เป็น error? หรือ NaN? -> 0 ปลอดภัยสุด
				} else {
					calcResult = dividend / divisor
				}

			default:
				log.Printf("WARN: Unknown calculation operation '%s' in formula '%s'. Skipping.", calcOp, t.Formula)
				calculationPossible = false // ทำการคำนวณไม่ได้
			}

			// เก็บผลลัพธ์ถ้าคำนวณสำเร็จ
			if calculationPossible {
				result[t.Field] = calcResult
				log.Printf("DEBUG: Calculation result for field '%s': %f", t.Field, calcResult)
			} else {
				log.Printf("WARN: Calculation for field '%s' was not possible due to errors in formula '%s'. Field not updated.", t.Field, t.Formula)
				continue
			}

		default:
			log.Printf("WARN: Unknown transformation operation '%s'. Skipping.", t.Operation)
		}
	}
	return result // คืน map ที่มีการเปลี่ยนแปลงแล้ว
}

// SubstituteVariables recursively replaces placeholders like $variableName in a template
// with values from the provided data map.
func SubstituteVariables(template interface{}, data map[string]interface{}) interface{} {
	if template == nil {
		return nil
	}

	switch t := template.(type) {
	case string:
		// ตรวจสอบว่าเป็น variable reference หรือไม่ (ขึ้นต้นด้วย $)
		if strings.HasPrefix(t, "$") {
			fieldPath := strings.TrimPrefix(t, "$")
			fieldParts := strings.Split(fieldPath, ".")

			// Traverse nested structure
			value := interface{}(data)
			for _, part := range fieldParts {
				if m, ok := value.(map[string]interface{}); ok {
					if val, exists := m[part]; exists {
						value = val
					} else {
						log.Printf("WARN: Nested field part '%s' not found in path '%s'", part, fieldPath)
						return nil
					}
				} else {
					log.Printf("WARN: Cannot traverse nested field '%s' in path '%s'", part, fieldPath)
					return nil
				}
			}
			log.Printf("TRACE: Substituting variable '%s' with value: %v", t, value)
			return value
		}
		return t

	case map[string]interface{}:
		// ถ้า template เป็น map, วน loop สร้าง map ใหม่แล้วแทนที่ค่าในแต่ละ value
		newMap := make(map[string]interface{})
		for k, v := range t {
			// เรียกตัวเองซ้ำสำหรับ value แต่ key ไม่ต้องแทนที่
			newMap[k] = SubstituteVariables(v, data)
		}
		return newMap

	case []interface{}:
		// ถ้า template เป็น slice, วน loop สร้าง slice ใหม่แล้วแทนที่ค่าในแต่ละ element
		newSlice := make([]interface{}, len(t))
		for i, v := range t {
			// เรียกตัวเองซ้ำสำหรับ element
			newSlice[i] = SubstituteVariables(v, data)
		}
		return newSlice

	// TODO: อาจจะรองรับ type อื่นๆ ถ้าจำเป็น เช่น []map[string]interface{} (แต่ logic map/slice ด้านบนน่าจะครอบคลุมแล้ว)

	default:
		// ถ้าเป็น type อื่นๆ ที่ไม่ได้ระบุไว้ (เช่น number, boolean) ให้คืนค่าเดิม
		return t
	}
}

// Helper function for 'calculate' operation.
// Tries to get a value from the data map if arg starts with '$',
// otherwise tries to parse arg as a literal float64.
func getValueAsFloat(arg string, data map[string]interface{}) (float64, bool) {
	if strings.HasPrefix(arg, "$") {
		// Support nested field access (e.g., $user.total.amount)
		fieldPath := strings.TrimPrefix(arg, "$")
		fieldParts := strings.Split(fieldPath, ".")

		// Traverse the nested structure
		value := interface{}(data)
		for _, part := range fieldParts {
			if m, ok := value.(map[string]interface{}); ok {
				if val, exists := m[part]; exists {
					value = val
				} else {
					log.Printf("TRACE: Nested field part '%s' not found in path '%s'", part, fieldPath)
					return 0, false
				}
			} else {
				log.Printf("TRACE: Cannot traverse nested field '%s' in path '%s'", part, fieldPath)
				return 0, false
			}
		}
		return convertToFloat64(value)
	}
	// Rest of the function remains the same...
	// ถ้าไม่ขึ้นต้นด้วย $ ให้ลอง parse เป็น float literal
	// ใช้ strconv.ParseFloat ที่ import มา
	// f, err := strconv.ParseFloat(arg, 64)
	// if err == nil {
	//     return f, true
	// }
	// ใช้ fmt.Sscan เพื่อความง่าย (อาจะไม่ strict เท่า strconv)
	var f float64
	_, err := fmt.Sscan(arg, &f)
	if err == nil {
		return f, true
	}

	log.Printf("TRACE: Argument '%s' could not be interpreted as a field reference or a float literal.", arg)
	return 0, false
}
