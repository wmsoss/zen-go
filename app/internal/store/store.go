// Package store: SQLite 存储层（商品 + 订单）。
//
// 纯 Go 驱动（modernc.org/sqlite），无需 cgo。数据库文件路径由 ZHONGER_DB
// 环境变量决定，默认 ./zhonger.db（systemd 下 WorkingDirectory=/opt/zhonger，
// 即 /opt/zhonger/zhonger.db）。Open 时自动建表，旧库自动补订单 token 列并回填。
//
// 订单状态只有两种：pending_payment（待支付）和 cancelled（已取消）。
// 在接入支付商之前不存在 paid 路径，订单永远停在 pending_payment。
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// 订单状态。
const (
	StatusPendingPayment = "pending_payment"
	StatusCancelled      = "cancelled"
)

// 哨兵错误：调用方据此映射 HTTP 状态码。
var (
	ErrNotFound      = errors.New("store: not found")
	ErrValidation    = errors.New("store: validation failed")
	ErrConflict      = errors.New("store: conflict")
	ErrInvalidStatus = errors.New("store: invalid status transition")
)

// MaxQuantity: 单笔订单最多件数，防刷单。
const MaxQuantity = 100

// MaxPriceCents: 单价上限（100 万美元等值），防误填。
const MaxPriceCents = 100_000_000

// Product: 商品。
type Product struct {
	ID          int64
	Slug        string
	Name        string
	Description string
	PriceCents  int64
	Currency    string
	Active      bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Order: 订单。商品名与单价在下单时快照，之后改价不影响已有订单。
// Token 是下单时生成的随机凭证：公共订单页只凭 token 访问，
// 自增 id 绝不直接暴露，避免无凭据枚举买家邮箱（O1）。
type Order struct {
	ID          int64
	Token       string
	ProductID   int64
	ProductName string
	PriceCents  int64
	Currency    string
	BuyerEmail  string
	Quantity    int
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TotalCents: 订单总价（分）。
func (o *Order) TotalCents() int64 { return o.PriceCents * int64(o.Quantity) }

// Store: 单连接 SQLite 封装（单连接避免多连接下的锁与 pragma 失效）。
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS products (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	slug TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	price_cents INTEGER NOT NULL CHECK (price_cents > 0),
	currency TEXT NOT NULL DEFAULT 'USD',
	active INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS orders (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	product_id INTEGER NOT NULL REFERENCES products(id),
	product_name TEXT NOT NULL,
	price_cents INTEGER NOT NULL,
	currency TEXT NOT NULL,
	buyer_email TEXT NOT NULL,
	token TEXT NOT NULL DEFAULT '',
	quantity INTEGER NOT NULL CHECK (quantity > 0),
	status TEXT NOT NULL DEFAULT 'pending_payment' CHECK (status IN ('pending_payment', 'cancelled')),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_orders_created_at ON orders(created_at DESC);
`

// Open: 打开数据库文件并建表。path 为 ":memory:" 时使用内存库。
func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create db dir: %w", err)
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	// 旧库补齐：给 2026-11-26 之前建的 orders 表加 token 列并回填，
	// 再建唯一索引。新库建表时 token 列已在 DDL 里，迁移是空操作。
	if err := migrateOrderTokens(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate order tokens: %w", err)
	}
	return &Store{db: db}, nil
}

// migrateOrderTokens: 旧库没有 token 列时补列；空 token 的历史订单逐行回填
// 随机 token；最后建唯一索引。调用方须保证单连接（Open 已 SetMaxOpenConns(1)）。
func migrateOrderTokens(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(orders)`)
	if err != nil {
		return err
	}
	hasToken := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "token" {
			hasToken = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !hasToken {
		if _, err := db.Exec(`ALTER TABLE orders ADD COLUMN token TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	ids, err := queryEmptyTokenOrderIDs(db)
	if err != nil {
		return err
	}
	for _, id := range ids {
		tok, err := newOrderToken()
		if err != nil {
			return err
		}
		if _, err := db.Exec(`UPDATE orders SET token = ? WHERE id = ?`, tok, id); err != nil {
			return err
		}
	}
	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_token ON orders(token)`)
	return err
}

func queryEmptyTokenOrderIDs(db *sql.DB) ([]int64, error) {
	rows, err := db.Query(`SELECT id FROM orders WHERE token = ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// newOrderToken: 16 字节随机数，hex 编码后 32 字符，可直接放 URL。
func newOrderToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

var tokenRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// validOrderToken: 只有 32 位小写 hex 才进库查询，其余一律按不存在处理。
func validOrderToken(tok string) bool { return tokenRe.MatchString(tok) }

// Close: 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

func dbTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseDBTime(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

var slugRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

// ValidateSlug: slug 只允许小写字母、数字与连字符。
func ValidateSlug(slug string) error {
	if len(slug) == 0 || len(slug) > 64 || !slugRe.MatchString(slug) {
		return fmt.Errorf("%w: invalid slug %q", ErrValidation, slug)
	}
	return nil
}

// ValidateEmail: 须是带点的域名地址，不接受显示名形式。
func ValidateEmail(email string) error {
	email = strings.TrimSpace(email)
	if email == "" || len(email) > 254 {
		return fmt.Errorf("%w: invalid email", ErrValidation)
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return fmt.Errorf("%w: invalid email %q", ErrValidation, email)
	}
	_, domain, ok := strings.Cut(email, "@")
	if !ok || !strings.Contains(domain, ".") {
		return fmt.Errorf("%w: invalid email %q", ErrValidation, email)
	}
	return nil
}

// ValidateProduct: 校验商品字段（不含 slug 唯一性，由存储层保证）。
func ValidateProduct(p *Product) error {
	if err := ValidateSlug(p.Slug); err != nil {
		return err
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Name) > 200 {
		return fmt.Errorf("%w: invalid product name", ErrValidation)
	}
	if len(p.Description) > 5000 {
		return fmt.Errorf("%w: description too long", ErrValidation)
	}
	if p.PriceCents <= 0 || p.PriceCents > MaxPriceCents {
		return fmt.Errorf("%w: invalid price_cents %d", ErrValidation, p.PriceCents)
	}
	if p.Currency == "" {
		p.Currency = "USD"
	}
	if !currencyRe.MatchString(p.Currency) {
		return fmt.Errorf("%w: invalid currency %q", ErrValidation, p.Currency)
	}
	return nil
}

func scanProduct(row interface {
	Scan(...any) error
}) (*Product, error) {
	var p Product
	var active int
	var created, updated string
	if err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &p.PriceCents, &p.Currency, &active, &created, &updated); err != nil {
		return nil, err
	}
	p.Active = active != 0
	var err error
	if p.CreatedAt, err = parseDBTime(created); err != nil {
		return nil, err
	}
	if p.UpdatedAt, err = parseDBTime(updated); err != nil {
		return nil, err
	}
	return &p, nil
}

func scanOrder(row interface {
	Scan(...any) error
}) (*Order, error) {
	var o Order
	var created, updated string
	if err := row.Scan(&o.ID, &o.ProductID, &o.ProductName, &o.PriceCents, &o.Currency, &o.BuyerEmail, &o.Quantity, &o.Status, &created, &updated, &o.Token); err != nil {
		return nil, err
	}
	var err error
	if o.CreatedAt, err = parseDBTime(created); err != nil {
		return nil, err
	}
	if o.UpdatedAt, err = parseDBTime(updated); err != nil {
		return nil, err
	}
	return &o, nil
}

const productColumns = `id, slug, name, description, price_cents, currency, active, created_at, updated_at`
const orderColumns = `id, product_id, product_name, price_cents, currency, buyer_email, quantity, status, created_at, updated_at, token`

// CreateProduct: 新增商品，slug 冲突返回 ErrConflict。
func (s *Store) CreateProduct(p *Product) error {
	if err := ValidateProduct(p); err != nil {
		return err
	}
	now := dbTime(time.Now())
	res, err := s.db.Exec(`INSERT INTO products (slug, name, description, price_cents, currency, active, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Slug, p.Name, p.Description, p.PriceCents, p.Currency, boolToInt(p.Active), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("%w: slug %q already exists", ErrConflict, p.Slug)
		}
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	p.ID = id
	p.CreatedAt, _ = parseDBTime(now)
	p.UpdatedAt = p.CreatedAt
	return nil
}

// GetProduct: 按 id 取商品。
func (s *Store) GetProduct(id int64) (*Product, error) {
	p, err := scanProduct(s.db.QueryRow(`SELECT `+productColumns+` FROM products WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// GetProductBySlug: 按 slug 取商品。
func (s *Store) GetProductBySlug(slug string) (*Product, error) {
	p, err := scanProduct(s.db.QueryRow(`SELECT `+productColumns+` FROM products WHERE slug = ?`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// ListProducts: 列出商品；activeOnly 为真时只列上架的。
func (s *Store) ListProducts(activeOnly bool) ([]*Product, error) {
	q := `SELECT ` + productColumns + ` FROM products`
	if activeOnly {
		q += ` WHERE active = 1`
	}
	q += ` ORDER BY id ASC`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProduct: 更新商品（slug 需保持唯一，冲突返回 ErrConflict）。
func (s *Store) UpdateProduct(p *Product) error {
	if err := ValidateProduct(p); err != nil {
		return err
	}
	var existingID int64
	err := s.db.QueryRow(`SELECT id FROM products WHERE slug = ?`, p.Slug).Scan(&existingID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// slug 未被占用，继续。
	case err != nil:
		return err
	case existingID != p.ID:
		return fmt.Errorf("%w: slug %q already exists", ErrConflict, p.Slug)
	}
	res, err := s.db.Exec(`UPDATE products SET slug = ?, name = ?, description = ?, price_cents = ?, currency = ?, active = ?, updated_at = ? WHERE id = ?`,
		p.Slug, p.Name, p.Description, p.PriceCents, p.Currency, boolToInt(p.Active), dbTime(time.Now()), p.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetProductActive: 上架/下架商品。
func (s *Store) SetProductActive(id int64, active bool) error {
	res, err := s.db.Exec(`UPDATE products SET active = ?, updated_at = ? WHERE id = ?`,
		boolToInt(active), dbTime(time.Now()), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateOrder: 下单。商品不存在返回 ErrNotFound，未上架/邮箱/数量非法返回
// ErrValidation。订单以 pending_payment 创建，并快照下单时的商品名与单价。
func (s *Store) CreateOrder(productID int64, buyerEmail string, quantity int) (*Order, error) {
	if err := ValidateEmail(buyerEmail); err != nil {
		return nil, err
	}
	if quantity < 1 || quantity > MaxQuantity {
		return nil, fmt.Errorf("%w: invalid quantity %d", ErrValidation, quantity)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, err := scanProduct(tx.QueryRow(`SELECT `+productColumns+` FROM products WHERE id = ?`, productID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !p.Active {
		return nil, fmt.Errorf("%w: product %q is not active", ErrValidation, p.Slug)
	}
	now := dbTime(time.Now())
	tok, err := newOrderToken()
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(`INSERT INTO orders (product_id, product_name, price_cents, currency, buyer_email, token, quantity, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.PriceCents, p.Currency, strings.TrimSpace(buyerEmail), tok, quantity, StatusPendingPayment, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	created, _ := parseDBTime(now)
	return &Order{
		ID:          id,
		Token:       tok,
		ProductID:   p.ID,
		ProductName: p.Name,
		PriceCents:  p.PriceCents,
		Currency:    p.Currency,
		BuyerEmail:  strings.TrimSpace(buyerEmail),
		Quantity:    quantity,
		Status:      StatusPendingPayment,
		CreatedAt:   created,
		UpdatedAt:   created,
	}, nil
}

// GetOrder: 按 id 取订单。只供后台管理（已鉴权）使用，公共端绝不用它。
func (s *Store) GetOrder(id int64) (*Order, error) {
	o, err := scanOrder(s.db.QueryRow(`SELECT `+orderColumns+` FROM orders WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return o, err
}

// GetOrderByToken: 公共订单页与结账的唯一取数口。token 格式不对或不存在
// 都返回 ErrNotFound，不区分两种情况，避免试探。
func (s *Store) GetOrderByToken(token string) (*Order, error) {
	if !validOrderToken(token) {
		return nil, ErrNotFound
	}
	o, err := scanOrder(s.db.QueryRow(`SELECT `+orderColumns+` FROM orders WHERE token = ?`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return o, err
}

// ListOrders: 列出全部订单，最新在前。
func (s *Store) ListOrders() ([]*Order, error) {
	rows, err := s.db.Query(`SELECT ` + orderColumns + ` FROM orders ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// CancelOrder: 取消订单。仅 pending_payment 可取消，已取消的再次取消
// 返回 ErrInvalidStatus，不存在的返回 ErrNotFound。
func (s *Store) CancelOrder(id int64) error {
	var status string
	err := s.db.QueryRow(`SELECT status FROM orders WHERE id = ?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != StatusPendingPayment {
		return fmt.Errorf("%w: order %d is %s", ErrInvalidStatus, id, status)
	}
	_, err = s.db.Exec(`UPDATE orders SET status = ?, updated_at = ? WHERE id = ?`,
		StatusCancelled, dbTime(time.Now()), id)
	return err
}
