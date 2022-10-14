package duckdb

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpen(t *testing.T) {
	t.Parallel()

	t.Run("without config", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		require.NotNil(t, db)
	})

	t.Run("with config", func(t *testing.T) {
		db, err := sql.Open("duckdb", "?access_mode=read_write&threads=4")
		require.NoError(t, err)
		defer db.Close()

		var (
			accessMode string
			threads    int64
		)
		res := db.QueryRow("SELECT current_setting('access_mode'), current_setting('threads')")
		require.NoError(t, res.Scan(&accessMode, &threads))
		require.Equal(t, int64(4), threads)
		require.Equal(t, "read_write", accessMode)
	})

	t.Run("with invalid config", func(t *testing.T) {
		db, _ := sql.Open("duckdb", "?threads=NaN")
		err := db.Ping()

		if !errors.Is(err, prepareConfigError) {
			t.Fatal("invalid config should not be accepted")
		}
	})
}

func TestExec(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()
	require.NotNil(t, createTable(db, t))
}

func TestQuery(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()
	createTable(db, t)

	t.Run("simple", func(t *testing.T) {
		res, err := db.Exec("INSERT INTO foo VALUES ('lala', ?), ('lalo', ?)", 12345, 1234)
		require.NoError(t, err)
		ra, err := res.RowsAffected()
		require.NoError(t, err)
		require.Equal(t, int64(2), ra)

		rows, err := db.Query("SELECT bar, baz FROM foo WHERE baz > ?", 12344)
		require.NoError(t, err)
		defer rows.Close()

		found := false
		for rows.Next() {
			var (
				bar string
				baz int
			)
			err := rows.Scan(&bar, &baz)
			require.NoError(t, err)
			if bar != "lala" || baz != 12345 {
				t.Errorf("wrong values for bar [%s] and baz [%d]", bar, baz)
			}
			found = true
		}

		require.True(t, found, "could not find row")
	})

	t.Run("large number of rows", func(t *testing.T) {
		_, err := db.Exec("CREATE TABLE integers AS SELECT * FROM range(0, 100000, 1) t(i);")
		require.NoError(t, err)

		rows, err := db.Query("SELECT i FROM integers ORDER BY i ASC")
		require.NoError(t, err)
		defer rows.Close()

		expected := 0
		for rows.Next() {
			var i int
			require.NoError(t, rows.Scan(&i))
			assert.Equal(t, expected, i)
			expected++
		}
	})

	t.Run("wrong syntax", func(t *testing.T) {
		_, err := db.Query("select * from tbl col=?", 1)
		require.Error(t, err, "should return error with invalid syntax")
	})

	t.Run("missing parameter", func(t *testing.T) {
		_, err := db.Query("select * from tbl col=?")
		require.Error(t, err, "should return error with missing parameters")
	})

	t.Run("null", func(t *testing.T) {
		var s *int
		require.NoError(t, db.QueryRow("select null").Scan(&s))
		require.Nil(t, s)
	})
}

func TestStruct(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()
	createTable(db, t)

	t.Run("scan single struct", func(t *testing.T) {
		row := db.QueryRow("SELECT {'x': 1, 'y': 2}")

		type point struct {
			X int
			Y int
		}

		want := Composite[point]{
			point{1, 2},
		}

		var result Composite[point]
		require.NoError(t, row.Scan(&result))
		require.Equal(t, want, result)
	})

	t.Run("scan slice of structs", func(t *testing.T) {
		_, err := db.Exec("INSERT INTO foo VALUES('lala', ?), ('lala', ?), ('lalo', 42), ('lalo', 43)", 12345, 12346)
		require.NoError(t, err)

		type row struct {
			Bar string
			Baz int
		}

		want := []Composite[row]{
			{row{"lalo", 42}},
			{row{"lalo", 43}},
			{row{"lala", 12345}},
			{row{"lala", 12346}},
		}

		rows, err := db.Query("SELECT ROW(bar, baz) FROM foo ORDER BY baz")
		require.NoError(t, err)
		defer rows.Close()

		var result []Composite[row]
		for rows.Next() {
			var res Composite[row]
			err := rows.Scan(&res)
			require.NoError(t, err)
			result = append(result, res)
		}
		require.Equal(t, want, result)
	})
}

func TestMap(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	t.Run("select map", func(t *testing.T) {
		var m Map
		require.NoError(t, db.QueryRow("SELECT map(['foo', 'bar'], ['a', 'e'])").Scan(&m))
		require.Equal(t, Map{
			"foo": "a",
			"bar": "e",
		}, m)
	})

	t.Run("insert map", func(t *testing.T) {
		tests := []struct {
			sqlType string
			input   Map
		}{
			// {
			// 	"MAP(VARCHAR, VARCHAR)",
			// 	Map{"foo": "bar"},
			// },
			// {
			// 	"MAP(INTEGER, VARCHAR)",
			// 	Map{int32(1): "bar"},
			// },
			// {
			// 	"MAP(VARCHAR, BIGINT)",
			// 	Map{"foo": int64(2)},
			// },
			// {
			// 	"MAP(VARCHAR, BOOLEAN)",
			// 	Map{"foo": true},
			// },
			// {
			// 	"MAP(DOUBLE, VARCHAR)",
			// 	Map{1.1: "foobar"},
			// },
		}
		for i, test := range tests {
			_, err := db.Exec(fmt.Sprintf("CREATE TABLE map_test_%d(properties %s)", i, test.sqlType))
			require.NoError(t, err)

			_, err = db.Exec(fmt.Sprintf("INSERT INTO map_test_%d VALUES(?)", i), test.input)
			require.NoError(t, err, test.input)

			var m Map
			require.NoError(t, db.QueryRow(fmt.Sprintf("SELECT properties FROM map_test_%d", i)).Scan(&m))
			require.Equal(t, test.input, m)
		}
	})
}

func TestList(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	t.Run("integer list", func(t *testing.T) {
		const n = 1000
		var row Composite[[]int]
		require.NoError(t, db.QueryRow("SELECT range(0, ?, 1)", n).Scan(&row))
		require.Equal(t, n, len(row.Get()))
		for i := 0; i < n; i++ {
			require.Equal(t, i, row.Get()[i])
		}
	})
}

func TestDecimal(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	t.Run("decimal widths", func(t *testing.T) {
		for i := 1; i <= 38; i++ {
			var f float64 = 999
			require.NoError(t, db.QueryRow(fmt.Sprintf("SELECT 0::DECIMAL(%d, 1)", i)).Scan(&f))
			require.Equal(t, float64(0), f)
		}
	})

	t.Run("multiple rows", func(t *testing.T) {
		rows, err := db.Query(`SELECT * FROM (VALUES
			(1.23 :: DECIMAL(3, 2)),
			(123.45 :: DECIMAL(5, 2)),
			(123456789.01 :: DECIMAL(11, 2)),
			(1234567890123456789.234 :: DECIMAL(22, 3)),
		) v
		ORDER BY v ASC`)
		require.NoError(t, err)
		defer rows.Close()

		want := []float64{1.23, 123.45, 123456789.01, 1234567890123456789.234}
		i := 0
		for rows.Next() {
			var fs float64
			require.NoError(t, rows.Scan(&fs))
			require.Equal(t, want[i], fs)
			i++
		}
	})

	t.Run("huge decimal", func(t *testing.T) {
		var f float64
		require.NoError(t, db.QueryRow("SELECT 123456789.01234567890123456789 :: DECIMAL(38, 20)").Scan(&f))
		require.True(t, math.Abs(float64(123456789.01234567890123456789)-f) < 0.0000001)
	})
}

func TestUUID(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	defer db.Close()

	_, err := db.Exec("CREATE TABLE uuid_test(uuid UUID)")
	require.NoError(t, err)

	tests := []uuid.UUID{
		uuid.New(),
		uuid.Nil,
		uuid.MustParse("80000000-0000-0000-0000-200000000000"),
	}
	for _, v := range tests {
		_, err := db.Exec("INSERT INTO uuid_test VALUES(?)", v)
		require.NoError(t, err)

		var uuid uuid.UUID
		require.NoError(t, db.QueryRow("SELECT uuid FROM uuid_test WHERE uuid = ?", v).Scan(&uuid))
		require.Equal(t, v, uuid)

		require.NoError(t, db.QueryRow("SELECT ?", v).Scan(&uuid))
		require.Equal(t, v, uuid)

		require.NoError(t, db.QueryRow("SELECT ?::uuid", v).Scan(&uuid))
		require.Equal(t, v, uuid)
	}
}

func TestENUMs(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	defer db.Close()

	_, err := db.Exec("CREATE TYPE element AS ENUM ('Sea', 'Air', 'Land')")
	require.NoError(t, err)

	_, err = db.Exec("CREATE TABLE vehicles (name text, environment element)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO vehicles VALUES (?, ?), (?, ?)", "Aircraft", "Air", "Boat", "Sea")
	require.NoError(t, err)

	var str string
	require.NoError(t, db.QueryRow("SELECT name FROM vehicles WHERE environment = ?", "Air").Scan(&str))
	require.Equal(t, "Aircraft", str)
}

func TestHugeInt(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	t.Run("scan HugeInt", func(t *testing.T) {
		for _, expected := range []int64{0, 1, -1, math.MaxInt64, math.MinInt64} {
			t.Run(fmt.Sprintf("sum(%d)", expected), func(t *testing.T) {
				var res HugeInt
				err := db.QueryRow("SELECT SUM(?)", expected).Scan(&res)
				require.NoError(t, err)

				expct, err := res.Int64()
				require.NoError(t, err)
				require.Equal(t, expected, expct)
			})
		}
	})

	t.Run("bind HugeInt", func(t *testing.T) {
		_, err := db.Exec("CREATE TABLE hugeint_test (number HUGEINT)")
		require.NoError(t, err)

		val := HugeInt{upper: 1, lower: 2}
		_, err = db.Exec("INSERT INTO hugeint_test VALUES(?)", val)
		require.NoError(t, err)

		var res HugeInt
		err = db.QueryRow("SELECT number FROM hugeint_test WHERE number = ?", val).Scan(&res)
		require.NoError(t, err)
		require.Equal(t, val, res)
	})
}

func TestVarchar(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	var s string

	t.Run("size is smaller than 12", func(t *testing.T) {
		require.NoError(t, db.QueryRow("select 'inline'").Scan(&s))
		require.Equal(t, "inline", s)
	})

	t.Run("size is exactly 12", func(t *testing.T) {
		require.NoError(t, db.QueryRow("select 'inlineSize12'").Scan(&s))
		require.Equal(t, "inlineSize12", s)
	})

	t.Run("size is 13", func(t *testing.T) {
		require.NoError(t, db.QueryRow("select 'inlineSize13_'").Scan(&s))
		require.Equal(t, "inlineSize13_", s)
	})

	t.Run("size is greater than 12", func(t *testing.T) {
		const q = "uninlined string that is greater than 12 bytes long"
		require.NoError(t, db.QueryRow(fmt.Sprintf("SELECT '%s'", q)).Scan(&s))
		require.Equal(t, q, s)
	})
}

func TestBlob(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	var bytes []byte

	t.Run("select as string", func(t *testing.T) {
		require.NoError(t, db.QueryRow("SELECT 'AB'::BLOB;").Scan(&bytes))
		require.Equal(t, "AB", string(bytes))
	})

	t.Run("select as hex", func(t *testing.T) {
		require.NoError(t, db.QueryRow("SELECT '\\xAA'::BLOB").Scan(&bytes))
		require.Equal(t, []byte{0xAA}, bytes)
	})
}

func TestBoolean(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	var res bool

	t.Run("scan", func(t *testing.T) {
		require.NoError(t, db.QueryRow("SELECT true").Scan(&res))
		require.Equal(t, true, res)

		require.NoError(t, db.QueryRow("SELECT false").Scan(&res))
		require.Equal(t, false, res)
	})

	t.Run("bind", func(t *testing.T) {
		require.NoError(t, db.QueryRow("SELECT ?", true).Scan(&res))
		require.Equal(t, true, res)

		require.NoError(t, db.QueryRow("SELECT ?", false).Scan(&res))
		require.Equal(t, false, res)

		require.NoError(t, db.QueryRow("SELECT ?", 0).Scan(&res))
		require.Equal(t, false, res)

		require.NoError(t, db.QueryRow("SELECT ?", 1).Scan(&res))
		require.Equal(t, true, res)
	})
}

func TestJSON(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	loadJSONExt(t, db)

	var data string

	t.Run("select empty JSON", func(t *testing.T) {
		require.NoError(t, db.QueryRow("SELECT '{}'::JSON").Scan(&data))
		require.Equal(t, "{}", string(data))
	})

	t.Run("select from marshalled JSON", func(t *testing.T) {
		val, _ := json.Marshal(struct {
			Foo string `json:"foo"`
		}{
			Foo: "bar",
		})
		require.NoError(t, db.QueryRow(`SELECT ?::JSON->>'foo'`, val).Scan(&data))
		require.Equal(t, "bar", data)
	})

	t.Run("select JSON array", func(t *testing.T) {
		require.NoError(t, db.QueryRow("SELECT json_array('foo', 'bar')").Scan(&data))
		require.Equal(t, `["foo","bar"]`, data)

		var items []string
		require.NoError(t, json.Unmarshal([]byte(data), &items))
		require.Equal(t, len(items), 2)
		require.Equal(t, items, []string{"foo", "bar"})
	})

}

// CAST(? as DATE) generate result of type Date (time.Time)
func TestDate(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()
	tests := map[string]struct {
		want  time.Time
		input string
	}{
		"epoch":       {input: "1970-01-01", want: time.UnixMilli(0).UTC()},
		"before 1970": {input: "1950-12-12", want: time.Date(1950, 12, 12, 0, 0, 0, 0, time.UTC)},
		"after 1970":  {input: "2022-12-12", want: time.Date(2022, 12, 12, 0, 0, 0, 0, time.UTC)},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var res time.Time
			err := db.QueryRow("SELECT CAST(? as DATE)", tc.input).Scan(&res)
			require.NoError(t, err)
			require.Equal(t, tc.want, res)
		})
	}
}

func TestTimestamp(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()
	tests := map[string]struct {
		input string
		want  time.Time
	}{
		"epoch":         {input: "1970-01-01", want: time.UnixMilli(0).UTC()},
		"before 1970":   {input: "1950-12-12", want: time.Date(1950, 12, 12, 0, 0, 0, 0, time.UTC)},
		"after 1970":    {input: "2022-12-12", want: time.Date(2022, 12, 12, 0, 0, 0, 0, time.UTC)},
		"HH:MM:SS":      {input: "2022-12-12 11:35:43", want: time.Date(2022, 12, 12, 11, 35, 43, 0, time.UTC)},
		"HH:MM:SS.DDDD": {input: "2022-12-12 11:35:43.5678", want: time.Date(2022, 12, 12, 11, 35, 43, 567800000, time.UTC)},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var res time.Time
			if err := db.QueryRow("SELECT CAST(? as TIMESTAMP)", tc.input).Scan(&res); err != nil {
				t.Errorf("can not scan value %v", err)
			} else if res != tc.want {
				t.Errorf("expected value %v != resulting value %v", tc.want, res)
			}
		})
	}
}

func TestInterval(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	t.Run("bind interval", func(t *testing.T) {
		interval := Interval{Days: 10, Months: 4, Micros: 4}
		row := db.QueryRow("SELECT ?::INTERVAL", interval)

		var res Interval
		require.NoError(t, row.Scan(&res))
		require.Equal(t, interval, res)
	})

	t.Run("scan interval", func(t *testing.T) {
		tests := map[string]struct {
			input string
			want  Interval
		}{
			"simple interval": {
				input: "INTERVAL 5 HOUR",
				want:  Interval{Days: 0, Months: 0, Micros: 18000000000},
			},
			"interval arithmetic": {
				input: "INTERVAL 1 DAY + INTERVAL 5 DAY",
				want:  Interval{Days: 6, Months: 0, Micros: 0},
			},
			"timestamp arithmetic": {
				input: "CAST('2022-05-01' as TIMESTAMP) - CAST('2022-04-01' as TIMESTAMP)",
				want:  Interval{Days: 30, Months: 0, Micros: 0},
			},
		}
		for name, tc := range tests {
			t.Run(name, func(t *testing.T) {
				var res Interval
				require.NoError(t, db.QueryRow(fmt.Sprintf("SELECT %s", tc.input)).Scan(&res))
				require.Equal(t, tc.want, res)
			})
		}
	})
}

func TestVariousLengthsVARCHAR(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	_, err := db.Exec(`CREATE TABLE values(id INTEGER, value VARCHAR)`)
	require.NoError(t, err)

	const maxLength = 1024
	values := []string{}
	for i := 0; i <= maxLength; i++ {
		values = append(values, randString(i))
	}

	for id, value := range values {
		_, err := db.Exec(`INSERT INTO values VALUES (?, ?)`, id, value)
		require.NoError(t, err)
	}

	rows, err := db.Query("SELECT id, value FROM values ORDER BY id ASC")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var id int
		var value string
		require.NoError(t, rows.Scan(&id, &value))
		assert.LessOrEqual(t, id, maxLength)
		assert.Equal(t, values[id], value)
		assert.Equal(t, len(values[id]), id)
	}
}

const (
	nRandomTestRows = 10000
)

func TestRandomizedUTINYINT(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	_, err := db.Exec(`CREATE TABLE values(id INTEGER, value UTINYINT)`)
	require.NoError(t, err)

	values := []uint8{}
	for i := 0; i < nRandomTestRows; i++ {
		values = append(values, uint8(randInt(0, 255)))
	}

	for id, value := range values {
		_, err := db.Exec(`INSERT INTO values VALUES (?, ?)`, id, value)
		require.NoError(t, err)
	}

	rows, err := db.Query("SELECT id, value FROM values ORDER BY id ASC")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var id int
		var value uint8
		require.NoError(t, rows.Scan(&id, &value))
		assert.LessOrEqual(t, id, nRandomTestRows)
		assert.Equal(t, values[id], value)
	}
}

func TestRandomizedTINYINT(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	_, err := db.Exec(`CREATE TABLE values(id INTEGER, value TINYINT)`)
	require.NoError(t, err)

	values := []int8{}
	for i := 0; i < nRandomTestRows; i++ {
		values = append(values, int8(randInt(-128, 127)))
	}

	for id, value := range values {
		_, err := db.Exec(`INSERT INTO values VALUES (?, ?)`, id, value)
		require.NoError(t, err)
	}

	rows, err := db.Query("SELECT id, value FROM values ORDER BY id ASC")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var id int
		var value int8
		require.NoError(t, rows.Scan(&id, &value))
		assert.LessOrEqual(t, id, nRandomTestRows)
		assert.Equal(t, values[id], value)
	}
}

func TestRandomizedUSMALLINT(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	_, err := db.Exec(`CREATE TABLE values(id INTEGER, value USMALLINT)`)
	require.NoError(t, err)

	values := []uint16{}
	for i := 0; i < nRandomTestRows; i++ {
		values = append(values, uint16(randInt(0, 65535)))
	}

	for id, value := range values {
		_, err := db.Exec(`INSERT INTO values VALUES (?, ?)`, id, value)
		require.NoError(t, err)
	}

	rows, err := db.Query("SELECT id, value FROM values ORDER BY id ASC")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var id int
		var value uint16
		require.NoError(t, rows.Scan(&id, &value))
		assert.LessOrEqual(t, id, nRandomTestRows)
		assert.Equal(t, values[id], value)
	}
}

func TestRandomizedSMALLINT(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	_, err := db.Exec(`CREATE TABLE values(id INTEGER, value SMALLINT)`)
	require.NoError(t, err)

	values := []int16{}
	for i := 0; i < nRandomTestRows; i++ {
		values = append(values, int16(randInt(-32768, 32767)))
	}

	for id, value := range values {
		_, err := db.Exec(`INSERT INTO values VALUES (?, ?)`, id, value)
		require.NoError(t, err)
	}

	rows, err := db.Query("SELECT id, value FROM values ORDER BY id ASC")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var id int
		var value int16
		require.NoError(t, rows.Scan(&id, &value))
		assert.LessOrEqual(t, id, nRandomTestRows)
		assert.Equal(t, values[id], value)
	}
}

func TestRandomizedUINTEGER(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	_, err := db.Exec(`CREATE TABLE values(id INTEGER, value UINTEGER)`)
	require.NoError(t, err)

	values := []uint32{}
	for i := 0; i < nRandomTestRows; i++ {
		values = append(values, uint32(randInt(0, 4294967295)))
	}

	for id, value := range values {
		_, err := db.Exec(`INSERT INTO values VALUES (?, ?)`, id, value)
		require.NoError(t, err)
	}

	rows, err := db.Query("SELECT id, value FROM values ORDER BY id ASC")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var id int
		var value uint32
		require.NoError(t, rows.Scan(&id, &value))
		assert.LessOrEqual(t, id, nRandomTestRows)
		assert.Equal(t, values[id], value)
	}
}

func TestRandomizedINTEGER(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	_, err := db.Exec(`CREATE TABLE values(id INTEGER, value INTEGER)`)
	require.NoError(t, err)

	values := []int32{}
	for i := 0; i < nRandomTestRows; i++ {
		values = append(values, int32(randInt(-2147483648, 2147483647)))
	}

	for id, value := range values {
		_, err := db.Exec(`INSERT INTO values VALUES (?, ?)`, id, value)
		require.NoError(t, err)
	}

	rows, err := db.Query("SELECT id, value FROM values ORDER BY id ASC")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var id int
		var value int32
		require.NoError(t, rows.Scan(&id, &value))
		assert.LessOrEqual(t, id, nRandomTestRows)
		assert.Equal(t, values[id], value)
	}
}

func TestRandomizedUBIGINT(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	_, err := db.Exec(`CREATE TABLE values(id INTEGER, value UBIGINT)`)
	require.NoError(t, err)

	values := []uint64{}
	for i := 0; i < nRandomTestRows; i++ {
		v := rand.Uint64()
		// go sql doesn't support uint64 values with high bit set (see for example https://github.com/lib/pq/issues/72)
		if v > 9223372036854775807 {
			v = 9223372036854775807
		}
		values = append(values, v)
	}

	for id, value := range values {
		_, err := db.Exec(`INSERT INTO values VALUES (?, ?)`, id, value)
		require.NoError(t, err)
	}

	rows, err := db.Query("SELECT id, value FROM values ORDER BY id ASC")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var id int
		var value uint64
		require.NoError(t, rows.Scan(&id, &value))
		assert.LessOrEqual(t, id, nRandomTestRows)
		assert.Equal(t, values[id], value)
	}
}

func TestRandomizedBIGINT(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	_, err := db.Exec(`CREATE TABLE values(id BIGINT, value BIGINT)`)
	require.NoError(t, err)

	values := []int64{}
	for i := 0; i < nRandomTestRows; i++ {
		values = append(values, rand.Int63())
	}

	for id, value := range values {
		_, err := db.Exec(`INSERT INTO values VALUES (?, ?)`, id, value)
		require.NoError(t, err)
	}

	rows, err := db.Query("SELECT id, value FROM values ORDER BY id ASC")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var id int
		var value int64
		require.NoError(t, rows.Scan(&id, &value))
		assert.LessOrEqual(t, id, nRandomTestRows)
		assert.Equal(t, values[id], value)
	}
}

func TestRandomizedREAL(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	_, err := db.Exec(`CREATE TABLE values(id INTEGER, value REAL)`)
	require.NoError(t, err)

	values := []float32{}
	for i := 0; i < nRandomTestRows; i++ {
		values = append(values, randFloat32(-3.4e+37, 3.4e+37))
	}

	for id, value := range values {
		_, err := db.Exec(`INSERT INTO values VALUES (?, ?)`, id, value)
		require.NoError(t, err)
	}

	rows, err := db.Query("SELECT id, value FROM values ORDER BY id ASC")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var id int
		var value float32
		require.NoError(t, rows.Scan(&id, &value))
		assert.LessOrEqual(t, id, nRandomTestRows)
		assert.True(t, float32similar(values[id], value))
	}
}

func TestRandomizedDOUBLE(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	_, err := db.Exec(`CREATE TABLE values(id INTEGER, value DOUBLE)`)
	require.NoError(t, err)

	values := []float64{}
	for i := 0; i < nRandomTestRows; i++ {
		values = append(values, randFloat64(-1.6e+307, 1.6e+307))
	}

	for id, value := range values {
		_, err := db.Exec(`INSERT INTO values VALUES (?, ?)`, id, value)
		require.NoError(t, err)
	}

	rows, err := db.Query("SELECT id, value FROM values ORDER BY id ASC")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var id int
		var value float64
		require.NoError(t, rows.Scan(&id, &value))
		assert.LessOrEqual(t, id, nRandomTestRows)
		assert.True(t, float64similar(values[id], value))
	}
}

func TestRandomizedVARCHAR(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	_, err := db.Exec(`CREATE TABLE values(id INTEGER, value VARCHAR)`)
	require.NoError(t, err)

	values := []string{}
	for i := 0; i < nRandomTestRows; i++ {
		values = append(values, randString(int(randInt(0, 256))))
	}

	for id, value := range values {
		_, err := db.Exec(`INSERT INTO values VALUES (?, ?)`, id, value)
		require.NoError(t, err)
	}

	rows, err := db.Query("SELECT id, value FROM values ORDER BY id ASC")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var id int
		var value string
		require.NoError(t, rows.Scan(&id, &value))
		assert.LessOrEqual(t, id, nRandomTestRows)
		assert.Equal(t, values[id], value)
	}
}

func TestRandomizedTIMESTAMP(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	_, err := db.Exec(`CREATE TABLE values(id INTEGER, value TIMESTAMP)`)
	require.NoError(t, err)

	values := []time.Time{}
	for i := 0; i < nRandomTestRows; i++ {
		values = append(values, randTime())
	}

	for id, value := range values {
		_, err := db.Exec(`INSERT INTO values VALUES (?, ?)`, id, value)
		require.NoError(t, err)
	}

	rows, err := db.Query("SELECT id, value FROM values ORDER BY id ASC")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var id int
		var value time.Time
		require.NoError(t, rows.Scan(&id, &value))
		assert.LessOrEqual(t, id, nRandomTestRows)
		assert.True(t, float32similar(float32(values[id].Unix()), float32(value.Unix())))
	}
}

func TestColumnTypeNames(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	_, err := db.Exec(`CREATE TABLE misc(id INTEGER, u8 UTINYINT, i8 TINYINT, u16 USMALLINT, i16 SMALLINT,
		u32 UINTEGER, i32 INTEGER, u64 UBIGINT, i64 BIGINT, hi HUGEINT,
		f REAL, d DOUBLE, s VARCHAR, b BOOLEAN)`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO misc VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 1, -1, 2, -2, 3, -3, 4, -4, 1234, 5.1, 6.2, "hello", true)
	require.NoError(t, err)

	rows, err := db.Query("SELECT * FROM misc ORDER BY id ASC")
	require.NoError(t, err)
	defer rows.Close()

	expectedDbTypeNames := []string{"INTEGER", "UTINYINT", "TINYINT", "USMALLINT", "SMALLINT",
		"UINTEGER", "INTEGER", "UBIGINT", "BIGINT", "HUGEINT",
		"FLOAT", "DOUBLE", "VARCHAR", "BOOLEAN", "TIMESTAMP"}
	colTypes, err := rows.ColumnTypes()
	for i, s := range colTypes {
		assert.NotNil(t, s)
		assert.Equal(t, expectedDbTypeNames[i], s.DatabaseTypeName())
	}
}

func TestEmpty(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	defer db.Close()

	rows, err := db.Query(`SELECT 1 WHERE 1 = 0`)
	require.NoError(t, err)
	defer rows.Close()
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
}

func TestMultiple(t *testing.T) {
	t.Parallel()

	db := openDB(t)
	defer db.Close()

	_, err := db.Exec("CREATE TABLE foo (x text); CREATE TABLE bar (x text);")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO foo VALUES (?); INSERT INTO bar VALUES (?);", "hello", "world")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Cannot prepare multiple statements at once")
}

func TestTypeNamesAndScanTypes(t *testing.T) {
	tests := []struct {
		sql      string
		value    any
		typeName string
	}{
		// DUCKDB_TYPE_BOOLEAN
		{
			sql:      "SELECT true AS col",
			value:    true,
			typeName: "BOOLEAN",
		},
		// DUCKDB_TYPE_TINYINT
		{
			sql:      "SELECT 31::TINYINT AS col",
			value:    int8(31),
			typeName: "TINYINT",
		},
		// DUCKDB_TYPE_SMALLINT
		{
			sql:      "SELECT 31::SMALLINT AS col",
			value:    int16(31),
			typeName: "SMALLINT",
		},
		// DUCKDB_TYPE_INTEGER
		{
			sql:      "SELECT 31::INTEGER AS col",
			value:    int32(31),
			typeName: "INTEGER",
		},
		// DUCKDB_TYPE_BIGINT
		{
			sql:      "SELECT 31::BIGINT AS col",
			value:    int64(31),
			typeName: "BIGINT",
		},
		// DUCKDB_TYPE_UTINYINT
		{
			sql:      "SELECT 31::UTINYINT AS col",
			value:    uint8(31),
			typeName: "UTINYINT",
		},
		// DUCKDB_TYPE_USMALLINT
		{
			sql:      "SELECT 31::USMALLINT AS col",
			value:    uint16(31),
			typeName: "USMALLINT",
		},
		// DUCKDB_TYPE_UINTEGER
		{
			sql:      "SELECT 31::UINTEGER AS col",
			value:    uint32(31),
			typeName: "UINTEGER",
		},
		// DUCKDB_TYPE_UBIGINT
		{
			sql:      "SELECT 31::UBIGINT AS col",
			value:    uint64(31),
			typeName: "UBIGINT",
		},
		// DUCKDB_TYPE_FLOAT
		{
			sql:      "SELECT 3.14::FLOAT AS col",
			value:    float32(3.14),
			typeName: "FLOAT",
		},
		// DUCKDB_TYPE_DOUBLE
		{
			sql:      "SELECT 3.14::DOUBLE AS col",
			value:    float64(3.14),
			typeName: "DOUBLE",
		},
		// DUCKDB_TYPE_TIMESTAMP
		{
			sql:      "SELECT '1992-09-20 11:30:00'::TIMESTAMP AS col",
			value:    time.Date(1992, 9, 20, 11, 30, 0, 0, time.UTC),
			typeName: "TIMESTAMP",
		},
		// DUCKDB_TYPE_DATE
		{
			sql:      "SELECT '1992-09-20'::DATE AS col",
			value:    time.Date(1992, 9, 20, 0, 0, 0, 0, time.UTC),
			typeName: "DATE",
		},
		// DUCKDB_TYPE_TIME
		{
			sql:      "SELECT '11:30:00'::TIME AS col",
			value:    time.Date(1970, 1, 1, 11, 30, 0, 0, time.UTC),
			typeName: "TIME",
		},
		// DUCKDB_TYPE_INTERVAL
		{
			sql:      "SELECT INTERVAL 15 MINUTES AS col",
			value:    Interval{Micros: 15 * 60 * 1000000},
			typeName: "INTERVAL",
		},
		// DUCKDB_TYPE_HUGEINT
		{
			sql:      "SELECT 31::HUGEINT AS col",
			value:    HugeInt{lower: 31, upper: 0},
			typeName: "HUGEINT",
		},
		// DUCKDB_TYPE_VARCHAR
		{
			sql:      "SELECT 'foo'::VARCHAR AS col",
			value:    "foo",
			typeName: "VARCHAR",
		},
		// DUCKDB_TYPE_BLOB
		{
			sql:      "SELECT 'foo'::BLOB AS col",
			value:    []byte("foo"),
			typeName: "BLOB",
		},
		// DUCKDB_TYPE_DECIMAL
		{
			sql:      "SELECT 31::DECIMAL(30,20) AS col",
			value:    float64(31),
			typeName: "DECIMAL(30,20)",
		},
		// DUCKDB_TYPE_TIMESTAMP_S
		{
			sql:      "SELECT '1992-09-20 11:30:00'::TIMESTAMP_S AS col",
			value:    time.Date(1992, 9, 20, 11, 30, 0, 0, time.UTC),
			typeName: "TIMESTAMP_S",
		},
		// DUCKDB_TYPE_TIMESTAMP_MS
		{
			sql:      "SELECT '1992-09-20 11:30:00'::TIMESTAMP_MS AS col",
			value:    time.Date(1992, 9, 20, 11, 30, 0, 0, time.UTC),
			typeName: "TIMESTAMP_MS",
		},
		// DUCKDB_TYPE_TIMESTAMP_NS
		{
			sql:      "SELECT '1992-09-20 11:30:00'::TIMESTAMP_NS AS col",
			value:    time.Date(1992, 9, 20, 11, 30, 0, 0, time.UTC),
			typeName: "TIMESTAMP_NS",
		},
		// DUCKDB_TYPE_LIST
		{
			sql:      "SELECT [['duck', 'goose', 'heron'], NULL, ['frog', 'toad'], []] AS col",
			value:    []any{[]any{"duck", "goose", "heron"}, nil, []any{"frog", "toad"}, []any{}},
			typeName: "VARCHAR[][]",
		},
		// DUCKDB_TYPE_STRUCT
		{
			sql:      "SELECT {'key1': 'string', 'key2': 1, 'key3': 12.345::DOUBLE} AS col",
			value:    map[string]any{"key1": "string", "key2": int32(1), "key3": float64(12.345)},
			typeName: "STRUCT(key1 VARCHAR, key2 INTEGER, key3 DOUBLE)",
		},
		// DUCKDB_TYPE_MAP
		{
			sql:      "SELECT map([1, 5], ['a', 'e']) AS col",
			value:    Map{int32(1): "a", int32(5): "e"},
			typeName: "MAP(INTEGER, VARCHAR)",
		},
		// DUCKDB_TYPE_UUID
		{
			sql:      "SELECT '53b4e983-b287-481a-94ad-6e3c90489913'::UUID AS col",
			value:    []byte{0x53, 0xb4, 0xe9, 0x83, 0xb2, 0x87, 0x48, 0x1a, 0x94, 0xad, 0x6e, 0x3c, 0x90, 0x48, 0x99, 0x13},
			typeName: "UUID",
		},
		// DUCKDB_TYPE_JSON
		{
			sql:      `SELECT '{"hello": "world"}'::JSON AS col`,
			value:    `{"hello": "world"}`,
			typeName: "JSON",
		},
	}

	db := openDB(t)
	for _, test := range tests {
		t.Run(test.typeName, func(t *testing.T) {
			rows, err := db.Query(test.sql)
			require.NoError(t, err)

			cols, err := rows.ColumnTypes()
			require.NoError(t, err)
			require.Equal(t, reflect.TypeOf(test.value), cols[0].ScanType())
			require.Equal(t, test.typeName, cols[0].DatabaseTypeName())

			var val any
			require.True(t, rows.Next())
			rows.Scan(&val)
			require.Equal(t, test.value, val)
		})
	}
}

func openDB(t *testing.T) *sql.DB {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	require.NoError(t, db.Ping())
	return db
}

func loadJSONExt(t *testing.T, db *sql.DB) {
	_, err := db.Exec("INSTALL 'json'")
	require.NoError(t, err)
	_, err = db.Exec("LOAD 'json'")
	require.NoError(t, err)
}

func createTable(db *sql.DB, t *testing.T) *sql.Result {
	res, err := db.Exec("CREATE TABLE foo(bar VARCHAR, baz INTEGER)")
	require.NoError(t, err)
	return &res
}

const (
	epsilon32 = 0.0000002
	epsilon64 = 0.0000000000002
)

func randInt(lo int64, hi int64) int64 {
	return rand.Int63n(hi-lo+1) + lo
}

func randBool() bool {
	return (rand.Int()%2 == 0)
}

func randFloat32(lo float32, hi float32) float32 {
	return (rand.Float32() * (hi - lo)) + lo
}

func randFloat64(lo float64, hi float64) float64 {
	return (rand.Float64() * (hi - lo)) + lo
}

func float32similar(v1 float32, v2 float32) bool {
	return math.Abs(float64(v2-v1)) < epsilon32
}

func float64similar(v1 float64, v2 float64) bool {
	return math.Abs(float64(v2-v1)) < epsilon64
}

var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

func randString(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func randTime() time.Time {
	lo := int64(788918400)  // 1995
	hi := int64(1735689600) // 2025
	randomEpoch := randInt(lo, hi)

	return time.Unix(randomEpoch, 0)
}
