package api

import (
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger" // สามารถเพิ่ม middleware อื่นๆ ที่นี่ได้
	// "github.com/gofiber/fiber/v2/middleware/cors" // ตัวอย่าง middleware เพิ่มเติม
)

// RegisterRoutes sets up the API routes using the provided handler
func RegisterRoutes(app *fiber.App, h *Handler) {

	// --- Middleware ---
	// คุณสามารถเพิ่ม Middleware ที่ต้องการให้ทำงานกับทุก Route ที่ลงทะเบียนในไฟล์นี้ได้ที่นี่
	// หรือจะไปเพิ่มใน main.go ก่อนเรียก RegisterRoutes ก็ได้
	app.Use(logger.New(logger.Config{
		// สามารถปรับแต่ง Format ของ Logger ได้ตามต้องการ
		Format: "[${ip}]:${port} ${status} - ${method} ${path}\n",
	}))
	// app.Use(cors.New()) // ตัวอย่างการเปิดใช้งาน CORS

	// --- Routes for managing API Definitions ---
	// จัดกลุ่ม route สำหรับจัดการ API definitions เพื่อความชัดเจน
	apiGenGroup := app.Group("/api-generator")

	apiGenGroup.Post("/create", h.CreateAPI)       // POST /api-generator/create
	apiGenGroup.Get("/list", h.ListAPIs)           // GET /api-generator/list
	apiGenGroup.Get("/detail/:name", h.GetAPIDetail) // GET /api-generator/detail/some-api-name
	apiGenGroup.Delete("/delete/:name", h.DeleteAPI) // DELETE /api-generator/delete/some-api-name
	apiGenGroup.Put("/update/:name", h.UpdateAPI)     // PUT /api-generator/update/some-api-name

	// Endpoint สำหรับ Reload API Definitions (ถ้าต้องการ implement)
	// apiGenGroup.Post("/reload", h.ReloadAPIs) // POST /api-generator/reload

	// --- Dynamic API Handler ---
	// Middleware/Handler นี้ควรลงทะเบียน **หลังสุด** สำหรับ path ที่ต้องการให้ dynamic API ทำงาน
	// การใช้ app.Use("/") จะทำให้ handler นี้ทำงานกับทุก request ที่ไม่ตรงกับ route ที่ลงทะเบียนไว้ก่อนหน้า
	// หากต้องการจำกัด dynamic routes ให้อยู่ภายใต้ path prefix เช่น /dynamic/ ก็สามารถใช้ app.Use("/dynamic", h.DynamicAPIHandler) ได้
	app.Use(h.DynamicAPIHandler)

	// สามารถเพิ่ม Route อื่นๆ ที่ไม่ใช่ dynamic API ได้ตามปกติ เช่น health check
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{"status": "ok"})
	})

}