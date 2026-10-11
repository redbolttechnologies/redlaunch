package application

// DatabaseSchema contains the user-visible relations in a PostgreSQL schema.
type DatabaseSchema struct {
	Name   string
	Tables []DatabaseTable
}

// DatabaseTable includes the prepared script used by the table context menu.
type DatabaseTable struct {
	Name string
	SQL  string
}

// DatabaseQueryResult holds CSV query output or command/script output.
type DatabaseQueryResult struct {
	Columns   []string
	Rows      [][]string
	Output    string
	Truncated bool
}
