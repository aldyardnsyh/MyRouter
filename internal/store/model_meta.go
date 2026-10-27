package store

// UpdateModelMeta updates catalog-derived metadata (context window, official
// prices, capabilities) without touching display name or availability.
func (db *DB) UpdateModelMeta(id string, contextWindow int, priceIn, priceOut *float64, capabilities string) error {
	_, err := db.Exec(`UPDATE model SET context_window=?, price_in=?, price_out=?, capabilities=? WHERE id=?`,
		contextWindow, priceIn, priceOut, capabilities, id)
	return err
}
