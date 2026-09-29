package mapdata

// SuppressCompetitiveWallItem is the local ordinary-rule supply policy. Oxygen
// has no useful ordinary combat effect. Keep the native catalog and pickup
// handlers intact for recorded games and other native rules.
func (entry CompetitiveMap) SuppressCompetitiveWallItem(sceneID uint32) bool {
	return entry.NativeRule == 1 && (sceneID == 26 || sceneID == 62)
}
