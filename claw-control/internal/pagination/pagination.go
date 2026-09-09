package pagination

const (
	DefaultLimit = 100
	MaxLimit     = 200
)

type Page[T any] struct {
	Items        []T
	NextBeforeID uint64
}

func Limit(value int) int {
	if value <= 0 {
		return DefaultLimit
	}
	if value > MaxLimit {
		return MaxLimit
	}
	return value
}

func Trim[T any](rows []T, limit int, id func(T) uint64) Page[T] {
	limit = Limit(limit)
	page := Page[T]{Items: rows}
	if len(rows) <= limit {
		return page
	}
	page.Items = rows[:limit]
	page.NextBeforeID = id(page.Items[len(page.Items)-1])
	return page
}
