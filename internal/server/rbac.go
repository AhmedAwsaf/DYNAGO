package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UserRecord represents a user stored in _dynago_users.
type UserRecord struct {
	ID        int       `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
}

// TablePermission represents permission flags for a role on a table.
type TablePermission struct {
	Role      string `json:"role"`
	TableName string `json:"table_name"`
	CanRead   bool   `json:"can_read"`
	CanInsert bool   `json:"can_insert"`
	CanUpdate bool   `json:"can_update"`
	CanDelete bool   `json:"can_delete"`
}

// InitRBAC ensures the metadata tables _dynago_users and _dynago_permissions exist
// and seeds the default admin user if the table is empty.
func InitRBAC(ctx context.Context, pool *pgxpool.Pool, defaultAdminUser, defaultAdminPass string) error {
	usersDDL := `
	CREATE TABLE IF NOT EXISTS _dynago_users (
		id SERIAL PRIMARY KEY,
		username VARCHAR(100) UNIQUE NOT NULL,
		password_hash VARCHAR(255) NOT NULL,
		role VARCHAR(50) NOT NULL DEFAULT 'viewer',
		is_active BOOLEAN NOT NULL DEFAULT TRUE,
		created_at TIMESTAMPTZ DEFAULT NOW()
	);`

	permsDDL := `
	CREATE TABLE IF NOT EXISTS _dynago_permissions (
		id SERIAL PRIMARY KEY,
		role VARCHAR(50) NOT NULL,
		table_name VARCHAR(100) NOT NULL,
		can_read BOOLEAN NOT NULL DEFAULT TRUE,
		can_insert BOOLEAN NOT NULL DEFAULT FALSE,
		can_update BOOLEAN NOT NULL DEFAULT FALSE,
		can_delete BOOLEAN NOT NULL DEFAULT FALSE,
		UNIQUE(role, table_name)
	);`

	if _, err := pool.Exec(ctx, usersDDL); err != nil {
		return fmt.Errorf("create _dynago_users: %w", err)
	}
	if _, err := pool.Exec(ctx, permsDDL); err != nil {
		return fmt.Errorf("create _dynago_permissions: %w", err)
	}

	tablePermsDDL := `
	CREATE TABLE IF NOT EXISTS _dynago_table_perms (
		table_name VARCHAR(100) PRIMARY KEY,
		is_public BOOLEAN NOT NULL DEFAULT FALSE,
		updated_at TIMESTAMPTZ DEFAULT NOW()
	);`
	if _, err := pool.Exec(ctx, tablePermsDDL); err != nil {
		log.Printf("[rbac] warning: create _dynago_table_perms: %v", err)
	}

	funcPermsDDL := `
	CREATE TABLE IF NOT EXISTS _dynago_function_perms (
		name VARCHAR(100) PRIMARY KEY,
		is_public BOOLEAN NOT NULL DEFAULT FALSE,
		updated_at TIMESTAMPTZ DEFAULT NOW()
	);`
	if _, err := pool.Exec(ctx, funcPermsDDL); err != nil {
		log.Printf("[rbac] warning: create _dynago_function_perms: %v", err)
	} else {
		_, _ = pool.Exec(ctx, `
			INSERT INTO _dynago_function_perms (name, is_public)
			VALUES 
				('currency_convert', TRUE),
				('get_order_receipt', TRUE),
				('create_order_receipt', TRUE),
				('update_order_status', TRUE)
			ON CONFLICT (name) DO NOTHING;
			DELETE FROM _dynago_function_perms WHERE name = 'get_order_receipts';
		`)
	}

	// Seed order mutation and receipt functions if orders table exists
	var ordersExists bool
	_ = pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'orders')").Scan(&ordersExists)
	if ordersExists {
		_, _ = pool.Exec(ctx, `
		DROP FUNCTION IF EXISTS get_order_receipts(INT);
		DROP FUNCTION IF EXISTS get_order_receipts();

		CREATE TABLE IF NOT EXISTS receipts (
			id SERIAL PRIMARY KEY,
			receipt_number VARCHAR(100) UNIQUE NOT NULL,
			order_id INT NOT NULL,
			customer_name VARCHAR(255),
			customer_email VARCHAR(255),
			total_amount NUMERIC(10, 2) NOT NULL,
			items_count INT NOT NULL DEFAULT 1,
			receipt_data JSONB NOT NULL,
			created_at TIMESTAMPTZ DEFAULT NOW()
		);

		CREATE OR REPLACE FUNCTION update_order_status(target_order_id INT, new_status VARCHAR)
		RETURNS JSON AS $$
		DECLARE
			v_id INT;
			v_order_number VARCHAR(100);
			v_status VARCHAR(100);
			v_total_amount NUMERIC(10, 2);
		BEGIN
			IF target_order_id IS NULL THEN
				RETURN json_build_object(
					'success', false,
					'error', 'Order not found',
					'order_id', NULL
				);
			END IF;
			IF new_status IS NULL OR new_status = '' THEN
				new_status := 'completed';
			END IF;

			UPDATE orders
			SET status = new_status
			WHERE id = target_order_id
			RETURNING id, order_number, status, total_amount 
			INTO v_id, v_order_number, v_status, v_total_amount;

			IF NOT FOUND THEN
				RETURN json_build_object(
					'success', false,
					'error', 'Order not found',
					'order_id', target_order_id
				);
			END IF;

			RETURN json_build_object(
				'success', true,
				'message', 'Order status updated successfully in database',
				'order_id', v_id,
				'order_number', v_order_number,
				'new_status', v_status,
				'total_amount', v_total_amount,
				'updated_at', NOW()
			);
		END;
		$$ LANGUAGE plpgsql VOLATILE;

		CREATE OR REPLACE FUNCTION create_order_receipt(target_order_id INT)
		RETURNS JSON AS $$
		DECLARE
			v_order_id INT;
			v_customer_id INT;
			v_order_number VARCHAR(100);
			v_status VARCHAR(100);
			v_total_amount NUMERIC(10, 2);
			v_placed_at TIMESTAMPTZ;
			v_customer_name VARCHAR(255) := 'Guest Customer';
			v_customer_email VARCHAR(255) := 'guest@example.com';
			v_items JSON := '[]'::json;
			v_items_count INT := 1;
			v_receipt_number VARCHAR(100);
			v_receipt JSON;
			v_tax NUMERIC(10, 2);
			v_final_total NUMERIC(10, 2);
		BEGIN
			IF target_order_id IS NULL THEN
				RETURN json_build_object(
					'success', false,
					'error', 'Order not found',
					'order_id', NULL
				);
			END IF;

			SELECT id, customer_id, order_number, status, total_amount, placed_at
			INTO v_order_id, v_customer_id, v_order_number, v_status, v_total_amount, v_placed_at
			FROM orders
			WHERE id = target_order_id;

			IF NOT FOUND THEN
				RETURN json_build_object(
					'success', false,
					'error', 'Order not found',
					'order_id', target_order_id
				);
			END IF;

			IF v_customer_id IS NOT NULL THEN
				SELECT COALESCE(full_name, 'Customer #' || v_customer_id::text), COALESCE(email, 'customer' || v_customer_id::text || '@example.com')
				INTO v_customer_name, v_customer_email
				FROM customers
				WHERE id = v_customer_id;
			END IF;

			SELECT 
				COALESCE(json_agg(json_build_object(
					'product_id', oi.product_id,
					'title', COALESCE(p.title, 'Product #' || oi.product_id::text),
					'sku', COALESCE(p.sku, 'SKU-' || oi.product_id::text),
					'quantity', oi.quantity,
					'unit_price', oi.unit_price,
					'line_total', ROUND((oi.quantity * oi.unit_price)::numeric, 2)
				)), '[]'::json),
				COALESCE(SUM(oi.quantity), 1)
			INTO v_items, v_items_count
			FROM order_items oi
			LEFT JOIN products p ON p.id = oi.product_id
			WHERE oi.order_id = target_order_id;

			v_receipt_number := 'REC-' || to_char(NOW(), 'YYYYMMDD') || '-' || LPAD(target_order_id::text, 4, '0');
			v_total_amount := COALESCE(v_total_amount, 0.00);
			v_tax := ROUND((v_total_amount * 0.08)::numeric, 2);
			v_final_total := ROUND((v_total_amount + v_tax)::numeric, 2);

			v_receipt := json_build_object(
				'receipt_number', v_receipt_number,
				'order_id', v_order_id,
				'order_number', v_order_number,
				'status', v_status,
				'customer', json_build_object(
					'customer_id', v_customer_id,
					'name', v_customer_name,
					'email', v_customer_email
				),
				'items', v_items,
				'total_items_count', v_items_count,
				'subtotal', v_total_amount,
				'tax', v_tax,
				'total_amount', v_final_total,
				'issued_at', NOW()
			);

			INSERT INTO receipts (
				receipt_number, order_id, customer_name, customer_email, total_amount, items_count, receipt_data, created_at
			) VALUES (
				v_receipt_number,
				v_order_id,
				v_customer_name,
				v_customer_email,
				v_final_total,
				v_items_count,
				v_receipt::jsonb,
				NOW()
			)
			ON CONFLICT (receipt_number) DO UPDATE
			SET receipt_data = EXCLUDED.receipt_data,
				total_amount = EXCLUDED.total_amount;

			RETURN json_build_object(
				'success', true,
				'message', 'Receipt created and saved to database successfully',
				'receipt', v_receipt
			);
		END;
		$$ LANGUAGE plpgsql VOLATILE;

		CREATE OR REPLACE FUNCTION get_order_receipt(target_order_id INT)
		RETURNS JSON AS $$
		DECLARE
			v_receipt JSON;
			v_gen_result JSON;
		BEGIN
			IF target_order_id IS NULL THEN
				RETURN json_build_object(
					'success', false,
					'error', 'Order not found',
					'order_id', NULL
				);
			END IF;

			SELECT receipt_data
			INTO v_receipt
			FROM receipts
			WHERE order_id = target_order_id
			ORDER BY id DESC
			LIMIT 1;

			IF v_receipt IS NULL THEN
				v_gen_result := create_order_receipt(target_order_id);
				IF (v_gen_result->>'success')::boolean = true THEN
					v_receipt := v_gen_result->'receipt';
				ELSE
					RETURN v_gen_result;
				END IF;
			END IF;

			RETURN v_receipt;
		END;
		$$ LANGUAGE plpgsql VOLATILE;
		`)
	}

	// Seed default admin if table is empty
	var userCount int
	err := pool.QueryRow(ctx, "SELECT count(*) FROM _dynago_users").Scan(&userCount)
	if err == nil && userCount == 0 && defaultAdminUser != "" && defaultAdminPass != "" {
		hash, err := hashPassword(defaultAdminPass)
		if err == nil {
			_, _ = pool.Exec(ctx,
				"INSERT INTO _dynago_users (username, password_hash, role) VALUES ($1, $2, 'admin') ON CONFLICT DO NOTHING",
				defaultAdminUser, hash,
			)
			log.Printf("[rbac] seeded initial admin user: %s", defaultAdminUser)
		}
	}

	return nil
}

// hashPassword creates a salted SHA-256 hash formatted as salt$hash.
func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h := sha256.Sum256(append(salt, []byte(password)...))
	return fmt.Sprintf("%x$%x", salt, h[:]), nil
}

// checkPassword verifies a password against a salt$hash string using constant-time comparison.
func checkPassword(password, storedHash string) bool {
	parts := strings.SplitN(storedHash, "$", 2)
	if len(parts) != 2 {
		return false
	}
	salt, err := hex.DecodeString(parts[0])
	if err != nil {
		return false
	}
	expectedHash, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	actualHash := sha256.Sum256(append(salt, []byte(password)...))
	return subtle.ConstantTimeCompare(actualHash[:], expectedHash) == 1
}

// authenticateUser checks the credentials in _dynago_users first,
// falling back to config credentials if needed.
func (h *handler) authenticateUser(ctx context.Context, username, password string) (*AuthUser, bool) {
	username = strings.TrimSpace(username)
	var (
		dbUser     string
		storedHash string
		role       string
		isActive   bool
	)

	err := h.pool.QueryRow(ctx,
		"SELECT username, password_hash, role, is_active FROM _dynago_users WHERE LOWER(username) = LOWER($1)",
		username,
	).Scan(&dbUser, &storedHash, &role, &isActive)

	if err == nil {
		if !isActive {
			return nil, false
		}
		if checkPassword(password, storedHash) {
			return &AuthUser{Username: dbUser, Role: role}, true
		}
		return nil, false
	}

	// Fallback to static admin from config
	userMatch := subtle.ConstantTimeCompare([]byte(username), []byte(h.cfg.AuthUser)) == 1
	passMatch := subtle.ConstantTimeCompare([]byte(password), []byte(h.cfg.AuthPass)) == 1
	if userMatch && passMatch {
		return &AuthUser{Username: h.cfg.AuthUser, Role: "admin"}, true
	}

	return nil, false
}

// checkTablePermission verifies whether the authenticated caller has permission for op on table.
// op can be: "read", "insert", "update", "delete".
func (h *handler) checkTablePermission(ctx context.Context, table, op string) error {
	// If authentication is disabled globally, allow everything
	if h.cfg.JWTSecret == "" {
		return nil
	}

	user, ok := UserFromContext(ctx)
	if !ok {
		// Not logged in. Check if table is designated as public
		if h.isTablePublic(ctx, table) {
			var (
				canRead   bool
				canInsert bool
				canUpdate bool
				canDelete bool
			)
			err := h.pool.QueryRow(ctx,
				`SELECT can_read, can_insert, can_update, can_delete 
				 FROM _dynago_permissions 
				 WHERE role = 'anon' AND table_name = $1`, table).Scan(&canRead, &canInsert, &canUpdate, &canDelete)
			if err != nil {
				// Default for public tables: anonymous users can read; mutation requires login
				if op == "read" {
					return nil
				}
				return fmt.Errorf("authentication required: %s on public table %q requires login", op, table)
			}

			switch op {
			case "read":
				if !canRead {
					return fmt.Errorf("anonymous read access disabled for table %q", table)
				}
			case "insert":
				if !canInsert {
					return fmt.Errorf("authentication required: insert on table %q requires login", table)
				}
			case "update":
				if !canUpdate {
					return fmt.Errorf("authentication required: update on table %q requires login", table)
				}
			case "delete":
				if !canDelete {
					return fmt.Errorf("authentication required: delete on table %q requires login", table)
				}
			}
			return nil
		}
		return fmt.Errorf("authentication required: table %q is private", table)
	}

	// Admin role has full access to all tables
	if user.Role == "admin" {
		return nil
	}

	// Check table-level permission in _dynago_permissions
	var (
		canRead   bool
		canInsert bool
		canUpdate bool
		canDelete bool
	)

	query := `SELECT can_read, can_insert, can_update, can_delete 
	          FROM _dynago_permissions 
	          WHERE role = $1 AND table_name = $2`
	err := h.pool.QueryRow(ctx, query, user.Role, table).Scan(&canRead, &canInsert, &canUpdate, &canDelete)

	if err != nil {
		// Default rule if no specific permission record exists for (role, table):
		switch user.Role {
		case "editor":
			// By default, editor has read-only access unless explicitly granted in permissions matrix
			if op != "read" {
				return fmt.Errorf("role %q does not have %s permission for table %q (not granted in permissions matrix)", user.Role, op, table)
			}
			return nil
		case "viewer":
			if op != "read" {
				return fmt.Errorf("role %q is read-only and cannot %s table %q", user.Role, op, table)
			}
			return nil
		default:
			return fmt.Errorf("role %q has no permissions configured for table %q", user.Role, table)
		}
	}

	switch op {
	case "read":
		if !canRead {
			return fmt.Errorf("role %q does not have read permission for table %q", user.Role, table)
		}
	case "insert":
		if !canInsert {
			return fmt.Errorf("role %q does not have insert permission for table %q", user.Role, table)
		}
	case "update":
		if !canUpdate {
			return fmt.Errorf("role %q does not have update permission for table %q", user.Role, table)
		}
	case "delete":
		if !canDelete {
			return fmt.Errorf("role %q does not have delete permission for table %q", user.Role, table)
		}
	}

	return nil
}

// --- User Management API Handlers (Admin Only) ---

// handleListUsers returns all registered users in _dynago_users.
func (h *handler) handleListUsers(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != "admin" {
		jsonError(w, http.StatusForbidden, "admin privileges required to manage users")
		return
	}

	rows, err := h.pool.Query(r.Context(),
		"SELECT id, username, role, is_active, created_at FROM _dynago_users ORDER BY id ASC")
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "query users: "+err.Error())
		return
	}
	defer rows.Close()

	var users []UserRecord
	for rows.Next() {
		var u UserRecord
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.IsActive, &u.CreatedAt); err != nil {
			continue
		}
		users = append(users, u)
	}

	if users == nil {
		users = []UserRecord{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(users)
}

// handleCreateUser adds a new user with a hashed password.
func (h *handler) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != "admin" {
		jsonError(w, http.StatusForbidden, "admin privileges required to create users")
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	bodyBytes = bytes.Trim(bytes.TrimSpace(bodyBytes), "'")

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request json: "+err.Error())
		return
	}

	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	req.Role = strings.ToLower(strings.TrimSpace(req.Role))
	if req.Username == "" || req.Password == "" {
		jsonError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	if req.Role == "" {
		req.Role = "viewer"
	}
	if req.Role != "admin" && req.Role != "editor" && req.Role != "viewer" {
		jsonError(w, http.StatusBadRequest, "invalid role: must be admin, editor, or viewer")
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "hash password error: "+err.Error())
		return
	}

	var newID int
	query := `INSERT INTO _dynago_users (username, password_hash, role) 
	          VALUES ($1, $2, $3) RETURNING id`
	err = h.pool.QueryRow(r.Context(), query, req.Username, hash, req.Role).Scan(&newID)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "create user error (username may already exist): "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"id":       newID,
		"username": req.Username,
		"role":     req.Role,
		"message":  "user created successfully",
	})
}

// handleUpdateUser updates user role, active status, or password.
func (h *handler) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != "admin" {
		jsonError(w, http.StatusForbidden, "admin privileges required to update users")
		return
	}

	target := strings.ToLower(strings.TrimSpace(r.PathValue("username")))
	if target == "" {
		jsonError(w, http.StatusBadRequest, "target username is required")
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	bodyBytes = bytes.Trim(bytes.TrimSpace(bodyBytes), "'")

	var req struct {
		Role     *string `json:"role"`
		IsActive *bool   `json:"is_active"`
		Password *string `json:"password"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request json: "+err.Error())
		return
	}

	// Verify target exists
	var (
		targetRole   string
		targetActive bool
	)
	err = h.pool.QueryRow(r.Context(),
		"SELECT role, is_active FROM _dynago_users WHERE LOWER(username) = LOWER($1)",
		target,
	).Scan(&targetRole, &targetActive)
	if err != nil {
		jsonError(w, http.StatusNotFound, "user not found: "+target)
		return
	}

	isRootAdmin := strings.EqualFold(target, h.cfg.AuthUser)
	isSelf := strings.EqualFold(target, user.Username)

	// Admin accounts cannot be demoted or deactivated by another admin
	if targetRole == "admin" && !isSelf {
		if (req.Role != nil && *req.Role != "admin") || (req.IsActive != nil && !*req.IsActive) {
			jsonError(w, http.StatusForbidden, "cannot demote or deactivate another administrator")
			return
		}
	}

	// Admins cannot demote or deactivate themselves
	if isSelf {
		if req.Role != nil && *req.Role != "admin" {
			jsonError(w, http.StatusBadRequest, "cannot demote your own administrator account")
			return
		}
		if req.IsActive != nil && !*req.IsActive {
			jsonError(w, http.StatusBadRequest, "cannot deactivate your own account")
			return
		}
	}

	// Root config admin protection
	if isRootAdmin {
		if req.Role != nil && *req.Role != "admin" {
			jsonError(w, http.StatusBadRequest, "cannot change role of root system administrator")
			return
		}
		if req.IsActive != nil && !*req.IsActive {
			jsonError(w, http.StatusBadRequest, "cannot deactivate root system administrator")
			return
		}
	}

	newRole := targetRole
	if req.Role != nil {
		rClean := strings.ToLower(strings.TrimSpace(*req.Role))
		if rClean != "admin" && rClean != "editor" && rClean != "viewer" {
			jsonError(w, http.StatusBadRequest, "invalid role: must be admin, editor, or viewer")
			return
		}
		newRole = rClean
	}

	newActive := targetActive
	if req.IsActive != nil {
		newActive = *req.IsActive
	}

	var newHash *string
	if req.Password != nil && strings.TrimSpace(*req.Password) != "" {
		hStr, err := hashPassword(strings.TrimSpace(*req.Password))
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "hash password: "+err.Error())
			return
		}
		newHash = &hStr
	}

	if newHash != nil {
		_, err = h.pool.Exec(r.Context(),
			"UPDATE _dynago_users SET role = $1, is_active = $2, password_hash = $3 WHERE LOWER(username) = LOWER($4)",
			newRole, newActive, *newHash, target)
	} else {
		_, err = h.pool.Exec(r.Context(),
			"UPDATE _dynago_users SET role = $1, is_active = $2 WHERE LOWER(username) = LOWER($3)",
			newRole, newActive, target)
	}

	if err != nil {
		jsonError(w, http.StatusInternalServerError, "update user error: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"message":   "user updated successfully",
		"username":  target,
		"role":      newRole,
		"is_active": newActive,
	})
}

// handleDeleteUser removes a user by username.
func (h *handler) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != "admin" {
		jsonError(w, http.StatusForbidden, "admin privileges required to delete users")
		return
	}

	target := strings.ToLower(strings.TrimSpace(r.PathValue("username")))
	if strings.EqualFold(target, user.Username) {
		jsonError(w, http.StatusBadRequest, "cannot delete your own user account")
		return
	}
	if strings.EqualFold(target, h.cfg.AuthUser) {
		jsonError(w, http.StatusBadRequest, "cannot delete root system administrator")
		return
	}

	var targetRole string
	err := h.pool.QueryRow(r.Context(),
		"SELECT role FROM _dynago_users WHERE LOWER(username) = LOWER($1)",
		target,
	).Scan(&targetRole)
	if err != nil {
		jsonError(w, http.StatusNotFound, "user not found: "+target)
		return
	}

	if targetRole == "admin" {
		jsonError(w, http.StatusForbidden, "cannot delete an administrator account — admin accounts are protected")
		return
	}

	tag, err := h.pool.Exec(r.Context(), "DELETE FROM _dynago_users WHERE LOWER(username) = LOWER($1)", target)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "delete user error: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		jsonError(w, http.StatusNotFound, "user not found: "+target)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"message":  "user deleted",
		"username": target,
	})
}

// --- Table Permissions API Handlers ---

// handleGetPermissions returns all table permissions for all roles.
func (h *handler) handleGetPermissions(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		jsonError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	_ = user

	rows, err := h.pool.Query(r.Context(),
		"SELECT role, table_name, can_read, can_insert, can_update, can_delete FROM _dynago_permissions ORDER BY role, table_name")
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "query permissions error: "+err.Error())
		return
	}
	defer rows.Close()

	var perms []TablePermission
	for rows.Next() {
		var p TablePermission
		if err := rows.Scan(&p.Role, &p.TableName, &p.CanRead, &p.CanInsert, &p.CanUpdate, &p.CanDelete); err != nil {
			continue
		}
		perms = append(perms, p)
	}

	if perms == nil {
		perms = []TablePermission{}
	}

	tableVis := make(map[string]bool)
	visRows, err := h.pool.Query(r.Context(), "SELECT table_name, is_public FROM _dynago_table_perms")
	if err == nil {
		defer visRows.Close()
		for visRows.Next() {
			var t string
			var pub bool
			if err := visRows.Scan(&t, &pub); err == nil {
				tableVis[t] = pub
			}
		}
	}
	for _, t := range h.graph.TableNames() {
		if _, ok := tableVis[t]; !ok {
			tableVis[t] = h.isTablePublic(r.Context(), t)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"roles":            []string{"admin", "editor", "viewer", "anon"},
		"tables":           h.graph.TableNames(),
		"table_visibility": tableVis,
		"permissions":      perms,
	})
}

// handleUpdatePermission upserts a permission rule for a role and table.
func (h *handler) handleUpdatePermission(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != "admin" {
		jsonError(w, http.StatusForbidden, "admin privileges required to configure permissions")
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	bodyBytes = bytes.Trim(bytes.TrimSpace(bodyBytes), "'")

	var req TablePermission
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request json: "+err.Error())
		return
	}

	req.Role = strings.TrimSpace(req.Role)
	req.TableName = strings.TrimSpace(req.TableName)
	if req.Role == "" || req.TableName == "" {
		jsonError(w, http.StatusBadRequest, "role and table_name are required")
		return
	}
	if req.Role == "admin" {
		jsonError(w, http.StatusBadRequest, "admin role always has full permissions on all tables")
		return
	}

	query := `
	INSERT INTO _dynago_permissions (role, table_name, can_read, can_insert, can_update, can_delete)
	VALUES ($1, $2, $3, $4, $5, $6)
	ON CONFLICT (role, table_name) 
	DO UPDATE SET 
		can_read = EXCLUDED.can_read,
		can_insert = EXCLUDED.can_insert,
		can_update = EXCLUDED.can_update,
		can_delete = EXCLUDED.can_delete;`

	_, err = h.pool.Exec(r.Context(), query,
		req.Role, req.TableName, req.CanRead, req.CanInsert, req.CanUpdate, req.CanDelete,
	)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "update permission error: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":     "updated",
		"permission": req,
	})
}

// handleUpdateFunctionPermission updates whether a function is public or private (Admin only).
func (h *handler) handleUpdateFunctionPermission(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != "admin" {
		jsonError(w, http.StatusForbidden, "admin privileges required to configure function visibility")
		return
	}

	name := r.PathValue("name")
	if name == "" {
		jsonError(w, http.StatusBadRequest, "function name required")
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	bodyBytes = bytes.Trim(bytes.TrimSpace(bodyBytes), "'")

	var req struct {
		IsPublic bool `json:"is_public"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request json: "+err.Error())
		return
	}

	query := `
	INSERT INTO _dynago_function_perms (name, is_public, updated_at)
	VALUES ($1, $2, NOW())
	ON CONFLICT (name) DO UPDATE SET is_public = EXCLUDED.is_public, updated_at = NOW();`

	_, err = h.pool.Exec(r.Context(), query, name, req.IsPublic)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "update function permission error: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"function":  name,
		"is_public": req.IsPublic,
		"status":    "updated",
	})
}

// isFunctionPublic checks if a function is public (accessible without login).
func (h *handler) isFunctionPublic(ctx context.Context, name string) bool {
	// 1. Check in database
	var isPublic bool
	err := h.pool.QueryRow(ctx, "SELECT is_public FROM _dynago_function_perms WHERE name = $1", name).Scan(&isPublic)
	if err == nil {
		return isPublic
	}

	// 2. Fallback to config DYNAGO_PUBLIC_FUNCTIONS
	if h.cfg != nil {
		for _, fn := range h.cfg.PublicFunctions {
			if strings.EqualFold(fn, name) {
				return true
			}
		}
	}

	// 3. Naming convention: names with "public" prefix/suffix
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "public_") || strings.HasSuffix(lower, "_public") {
		return true
	}

	return false
}

// handleUpdateTableVisibility updates whether a table is public or private (Admin only).
func (h *handler) handleUpdateTableVisibility(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok || user.Role != "admin" {
		jsonError(w, http.StatusForbidden, "admin privileges required to configure table visibility")
		return
	}

	name := r.PathValue("name")
	if name == "" {
		jsonError(w, http.StatusBadRequest, "table name required")
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	bodyBytes = bytes.Trim(bytes.TrimSpace(bodyBytes), "'")

	var req struct {
		IsPublic bool `json:"is_public"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request json: "+err.Error())
		return
	}

	query := `
	INSERT INTO _dynago_table_perms (table_name, is_public, updated_at)
	VALUES ($1, $2, NOW())
	ON CONFLICT (table_name) DO UPDATE SET is_public = EXCLUDED.is_public, updated_at = NOW();`

	_, err = h.pool.Exec(r.Context(), query, name, req.IsPublic)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "update table visibility error: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"table":     name,
		"is_public": req.IsPublic,
		"status":    "updated",
	})
}

// isTablePublic checks if a table is public (accessible without login).
func (h *handler) isTablePublic(ctx context.Context, table string) bool {
	// 1. Check in database
	var isPublic bool
	err := h.pool.QueryRow(ctx, "SELECT is_public FROM _dynago_table_perms WHERE table_name = $1", table).Scan(&isPublic)
	if err == nil {
		return isPublic
	}

	// 2. Fallback to config DYNAGO_PUBLIC_TABLES
	if h.cfg != nil {
		for _, t := range h.cfg.PublicTables {
			if strings.EqualFold(t, table) {
				return true
			}
		}
	}

	// 3. Naming convention: names with "public" prefix/suffix
	lower := strings.ToLower(table)
	if strings.HasPrefix(lower, "public_") || strings.HasSuffix(lower, "_public") {
		return true
	}

	return false
}

// filterAccessibleTables returns the list of table names the caller in ctx is permitted to view.
func (h *handler) filterAccessibleTables(ctx context.Context) []string {
	all := h.graph.TableNames()
	if h.cfg.JWTSecret == "" {
		sort.Strings(all)
		return all
	}

	user, ok := UserFromContext(ctx)
	if ok && user.Role == "admin" {
		sort.Strings(all)
		return all
	}

	var accessible []string
	for _, t := range all {
		if err := h.checkTablePermission(ctx, t, "read"); err == nil {
			accessible = append(accessible, t)
		}
	}
	sort.Strings(accessible)
	return accessible
}

