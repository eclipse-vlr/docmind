package postgres

import "strconv"

// Pass vectors as a bound text parameter, then cast server-side. No pgx codec
// registration or extra Go dependency is needed for the fixed vector type.
func vectorString(vector []float32) string {
	buf := make([]byte, 0, len(vector)*8)
	buf = append(buf, '[')
	for i, v := range vector {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendFloat(buf, float64(v), 'g', -1, 32)
	}
	buf = append(buf, ']')
	return string(buf)
}
