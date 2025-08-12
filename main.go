package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/mux"
)

type Order struct {
	ID          int
	OrderID     string
	CustomerID  string
	Size        string
	Quantity    int
	TotalAmount float64
	Status      string
	CreatedAt   string
}

var db *sql.DB

var priceMap = map[string]float64{
	"XS": 600, "S": 800, "M": 900, "L": 1000, "XL": 1100, "XXL": 1200,
}
var statuses = []string{"PROCESSING", "DELIVERING", "DELIVERED"}

// generateOrderID - simple generator using DB's last insert id is tricky, so we make a timestamp-like code.
// For production, consider UUID or a safer sequence in DB.
func generateOrderID(nextSeq int) string {
	return fmt.Sprintf("ODR#%05d", nextSeq)
}

func mustParseTemplates(name string) *template.Template {
	return template.Must(template.ParseFiles("templates/" + name))
}

func home(w http.ResponseWriter, r *http.Request) {
	t := mustParseTemplates("home.html")
	_ = t.Execute(w, nil)
}

// placeOrder: GET -> form, POST -> insert order into DB
func placeOrderPage(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		t := mustParseTemplates("form.html")
		_ = t.Execute(w, nil)
		return
	}

	if r.Method == http.MethodPost {
		contact := r.FormValue("contact")
		size := r.FormValue("size")
		qty, err := strconv.Atoi(r.FormValue("qty"))
		if err != nil {
			http.Error(w, "Quantity must be a number", http.StatusBadRequest)
			return
		}
		price, ok := priceMap[size]
		if !ok {
			http.Error(w, "Invalid size", http.StatusBadRequest)
			return
		}
		amount := price * float64(qty)

		// Use a DB transaction to get a sequence-like number for OrderID
		tx, err := db.Begin()
		if err != nil {
			http.Error(w, "DB error", http.StatusInternalServerError)
			return
		}
		// Insert a placeholder row to get auto-increment id
		res, err := tx.Exec("INSERT INTO orders (order_id, customer_id, size, quantity, total_amount, status) VALUES (?, ?, ?, ?, ?, ?)",
			"", contact, size, qty, amount, statuses[0])
		if err != nil {
			tx.Rollback()
			http.Error(w, "DB insert error", http.StatusInternalServerError)
			return
		}
		lastID, err := res.LastInsertId()
		if err != nil {
			tx.Rollback()
			http.Error(w, "DB id error", http.StatusInternalServerError)
			return
		}

		orderCode := generateOrderID(int(lastID))
		_, err = tx.Exec("UPDATE orders SET order_id = ? WHERE id = ?", orderCode, lastID)
		if err != nil {
			tx.Rollback()
			http.Error(w, "DB update error", http.StatusInternalServerError)
			return
		}
		err = tx.Commit()
		if err != nil {
			http.Error(w, "DB commit error", http.StatusInternalServerError)
			return
		}

		// Build order object to pass to template
		order := Order{
			ID:          int(lastID),
			OrderID:     orderCode,
			CustomerID:  contact,
			Size:        size,
			Quantity:    qty,
			TotalAmount: amount,
			Status:      statuses[0],
		}

		t := mustParseTemplates("success.html")
		_ = t.Execute(w, order)
	}
}

// search customer
func searchCustomerPage(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		t := mustParseTemplates("search_customer_form.html")
		_ = t.Execute(w, nil)
		return
	}

	contact := r.FormValue("contact")
	rows, err := db.Query("SELECT id, order_id, customer_id, size, quantity, total_amount, status, created_at FROM orders WHERE customer_id = ?", contact)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var found []Order
	for rows.Next() {
		var o Order
		_ = rows.Scan(&o.ID, &o.OrderID, &o.CustomerID, &o.Size, &o.Quantity, &o.TotalAmount, &o.Status, &o.CreatedAt)
		found = append(found, o)
	}
	t := mustParseTemplates("search_customer_results.html")
	_ = t.Execute(w, found)
}

// search order
func searchOrderPage(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		t := mustParseTemplates("search_order_form.html")
		_ = t.Execute(w, nil)
		return
	}

	orderID := strings.TrimSpace(r.FormValue("orderid"))
	if orderID == "" {
		http.Error(w, "Order ID required", http.StatusBadRequest)
		return
	}
	row := db.QueryRow("SELECT id, order_id, customer_id, size, quantity, total_amount, status, created_at FROM orders WHERE order_id = ?", orderID)
	var o Order
	err := row.Scan(&o.ID, &o.OrderID, &o.CustomerID, &o.Size, &o.Quantity, &o.TotalAmount, &o.Status, &o.CreatedAt)
	if err == sql.ErrNoRows {
		t := mustParseTemplates("order_not_found.html")
		_ = t.Execute(w, nil)
		return
	} else if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	t := mustParseTemplates("search_order_results.html")
	_ = t.Execute(w, o)
}

// reports
type ReportData struct {
	Orders      []Order
	TotalOrders int
	TotalAmount float64
}

func viewReports(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT id, order_id, customer_id, size, quantity, total_amount, status, created_at FROM orders ORDER BY created_at DESC")
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var orders []Order
	var total float64
	for rows.Next() {
		var o Order
		_ = rows.Scan(&o.ID, &o.OrderID, &o.CustomerID, &o.Size, &o.Quantity, &o.TotalAmount, &o.Status, &o.CreatedAt)
		orders = append(orders, o)
		total += o.TotalAmount
	}

	data := ReportData{
		Orders:      orders,
		TotalOrders: len(orders),
		TotalAmount: total,
	}
	t := mustParseTemplates("reports.html")
	_ = t.Execute(w, data)
}

// change status
func changeStatusPage(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		rows, err := db.Query("SELECT id, order_id, customer_id, size, quantity, total_amount, status, created_at FROM orders ORDER BY created_at DESC")
		if err != nil {
			http.Error(w, "DB error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		var orders []Order
		for rows.Next() {
			var o Order
			_ = rows.Scan(&o.ID, &o.OrderID, &o.CustomerID, &o.Size, &o.Quantity, &o.TotalAmount, &o.Status, &o.CreatedAt)
			orders = append(orders, o)
		}
		t := mustParseTemplates("change_status_form.html")
		_ = t.Execute(w, orders)
		return
	}

	idStr := r.FormValue("orderid")
	// Allow either order_id or numeric id — here we expect order id string
	orderID := idStr
	// find current status
	row := db.QueryRow("SELECT status FROM orders WHERE order_id = ?", orderID)
	var currentStatus string
	err := row.Scan(&currentStatus)
	if err == sql.ErrNoRows {
		t := mustParseTemplates("status_error.html")
		_ = t.Execute(w, nil)
		return
	} else if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}

	var newStatus string
	if currentStatus == "PROCESSING" {
		newStatus = "DELIVERING"
	} else if currentStatus == "DELIVERING" {
		newStatus = "DELIVERED"
	} else {
		t := mustParseTemplates("status_error.html")
		_ = t.Execute(w, nil)
		return
	}

	_, err = db.Exec("UPDATE orders SET status = ? WHERE order_id = ?", newStatus, orderID)
	if err != nil {
		http.Error(w, "DB update error", http.StatusInternalServerError)
		return
	}

	row2 := db.QueryRow("SELECT id, order_id, customer_id, size, quantity, total_amount, status, created_at FROM orders WHERE order_id = ?", orderID)
	var o Order
	_ = row2.Scan(&o.ID, &o.OrderID, &o.CustomerID, &o.Size, &o.Quantity, &o.TotalAmount, &o.Status, &o.CreatedAt)

	t := mustParseTemplates("status_updated.html")
	_ = t.Execute(w, o)
}

// delete
func deleteOrderPage(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		rows, err := db.Query("SELECT id, order_id, customer_id, size, quantity, total_amount, status, created_at FROM orders ORDER BY created_at DESC")
		if err != nil {
			http.Error(w, "DB error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		var orders []Order
		for rows.Next() {
			var o Order
			_ = rows.Scan(&o.ID, &o.OrderID, &o.CustomerID, &o.Size, &o.Quantity, &o.TotalAmount, &o.Status, &o.CreatedAt)
			orders = append(orders, o)
		}
		t := mustParseTemplates("delete_order_form.html")
		_ = t.Execute(w, orders)
		return
	}

	orderID := r.FormValue("orderid")
	res, err := db.Exec("DELETE FROM orders WHERE order_id = ?", orderID)
	if err != nil {
		http.Error(w, "DB delete error", http.StatusInternalServerError)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		t := mustParseTemplates("order_not_found.html")
		_ = t.Execute(w, nil)
		return
	}

	t := mustParseTemplates("order_deleted.html")
	_ = t.Execute(w, struct{ OrderID string }{OrderID: orderID})
}

func main() {
	// Open DB connection (replace user:pass with yours)
	var err error
	dsn := "root:1234@tcp(127.0.0.1:3306)/orderdb?parseTime=true"
	db, err = sql.Open("mysql", dsn)
	if err != nil {
		log.Fatalf("DB open error: %v", err)
	}
	defer db.Close()

	if err = db.Ping(); err != nil {
		log.Fatalf("DB ping error: %v", err)
	}

	r := mux.NewRouter()
	r.HandleFunc("/", home).Methods("GET")
	r.HandleFunc("/place-order", placeOrderPage).Methods("GET", "POST")
	r.HandleFunc("/search-customer", searchCustomerPage).Methods("GET", "POST")
	r.HandleFunc("/search-order", searchOrderPage).Methods("GET", "POST")
	r.HandleFunc("/reports", viewReports).Methods("GET")
	r.HandleFunc("/change-status", changeStatusPage).Methods("GET", "POST")
	r.HandleFunc("/delete-order", deleteOrderPage).Methods("GET", "POST")

	fmt.Println("Server running at http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}
