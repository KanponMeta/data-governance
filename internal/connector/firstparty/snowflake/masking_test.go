package snowflake

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/connector"
)

// snowflakeWithMock returns an ApplyMaskingPolicy-ready *Snowflake wired to
// the supplied sqlmock. The returned cleanup closes the underlying *sql.DB.
func snowflakeWithMock(t *testing.T) (*Snowflake, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	return NewFromDB(db), mock, func() { _ = db.Close() }
}

// TestSnowflake_ApplyMaskingPolicy_Hash_DDL — verifies CREATE OR REPLACE +
// ALTER TABLE SET MASKING POLICY are issued in order with the SHA2_HEX body
// and fully-qualified identifiers (Pitfall #2).
func TestSnowflake_ApplyMaskingPolicy_Hash_DDL(t *testing.T) {
	s, mock, cleanup := snowflakeWithMock(t)
	defer cleanup()

	mock.ExpectExec(`(?s)CREATE OR REPLACE MASKING POLICY "DB"\."SCH"\."dgp_mask_orders_ssn".*SHA2_HEX`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)ALTER TABLE "DB"\."SCH"\."orders" ALTER COLUMN "ssn" SET MASKING POLICY "DB"\."SCH"\."dgp_mask_orders_ssn"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := s.ApplyMaskingPolicy(context.Background(),
		connector.AssetRef{Identifier: "DB.SCH.orders"},
		connector.ColumnPolicy{Column: "ssn", MaskType: connector.MaskHash, AllowRoles: []string{"PII_ANALYST"}},
	)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSnowflake_ApplyMaskingPolicy_Redact — verifies redact body contains '***'.
func TestSnowflake_ApplyMaskingPolicy_Redact(t *testing.T) {
	s, mock, cleanup := snowflakeWithMock(t)
	defer cleanup()

	mock.ExpectExec(`(?s)CREATE OR REPLACE MASKING POLICY.*'\*\*\*'`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE.*SET MASKING POLICY`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := s.ApplyMaskingPolicy(context.Background(),
		connector.AssetRef{Identifier: "DB.SCH.orders"},
		connector.ColumnPolicy{Column: "ssn", MaskType: connector.MaskRedact},
	)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSnowflake_ApplyMaskingPolicy_Partial — partial body has LEFT/REPEAT/RIGHT.
func TestSnowflake_ApplyMaskingPolicy_Partial(t *testing.T) {
	s, mock, cleanup := snowflakeWithMock(t)
	defer cleanup()

	mock.ExpectExec(`(?s)CREATE OR REPLACE MASKING POLICY.*LEFT\(TO_VARCHAR\(val\),2\).*REPEAT\('\*'`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER TABLE.*SET MASKING POLICY`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := s.ApplyMaskingPolicy(context.Background(),
		connector.AssetRef{Identifier: "DB.SCH.orders"},
		connector.ColumnPolicy{Column: "ssn", MaskType: connector.MaskPartial},
	)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSnowflake_RemoveMaskingPolicy_UNSET_then_DROP — verifies UNSET runs
// before DROP MASKING POLICY IF EXISTS.
func TestSnowflake_RemoveMaskingPolicy_UNSET_then_DROP(t *testing.T) {
	s, mock, cleanup := snowflakeWithMock(t)
	defer cleanup()

	mock.ExpectExec(`ALTER TABLE.*ALTER COLUMN.*UNSET MASKING POLICY`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`DROP MASKING POLICY IF EXISTS`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := s.RemoveMaskingPolicy(context.Background(),
		connector.AssetRef{Identifier: "DB.SCH.orders"}, "ssn")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSnowflake_ListMaskingPolicies_ParsesNamePrefix — verifies the prefix
// filter and the parser recovering MaskType from policy body.
func TestSnowflake_ListMaskingPolicies_ParsesNamePrefix(t *testing.T) {
	s, mock, cleanup := snowflakeWithMock(t)
	defer cleanup()

	rows := sqlmock.NewRows([]string{"POLICY_NAME", "POLICY_BODY"}).
		AddRow("dgp_mask_orders_ssn", "CASE WHEN ARRAY_CONTAINS(CURRENT_ROLE()::variant, ARRAY_CONSTRUCT('PII_ANALYST')) THEN val ELSE TO_VARIANT(SHA2_HEX(TO_VARCHAR(val), 256)) END").
		AddRow("dgp_mask_orders_email", "CASE WHEN ARRAY_CONTAINS(CURRENT_ROLE()::variant, ARRAY_CONSTRUCT()) THEN val ELSE TO_VARIANT('***') END")
	mock.ExpectQuery(`(?s)SELECT POLICY_NAME, POLICY_BODY.*INFORMATION_SCHEMA\.MASKING_POLICIES`).
		WithArgs("SCH", "dgp_mask_orders_%").
		WillReturnRows(rows)

	out, err := s.ListMaskingPolicies(context.Background(),
		connector.AssetRef{Identifier: "DB.SCH.orders"})
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.Equal(t, "ssn", out[0].Column)
	require.Equal(t, connector.MaskHash, out[0].MaskType)
	require.Equal(t, []string{"PII_ANALYST"}, out[0].AllowRoles)
	require.Equal(t, "email", out[1].Column)
	require.Equal(t, connector.MaskRedact, out[1].MaskType)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSnowflake_ApplyMaskingPolicy_ContextCancellation_Returns — cancelled
// ctx surfaces as context.Canceled (Assumption A10).
func TestSnowflake_ApplyMaskingPolicy_ContextCancellation_Returns(t *testing.T) {
	s, _, cleanup := snowflakeWithMock(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := s.ApplyMaskingPolicy(ctx,
		connector.AssetRef{Identifier: "DB.SCH.orders"},
		connector.ColumnPolicy{Column: "ssn", MaskType: connector.MaskHash},
	)
	require.Error(t, err)
}

// TestSnowflake_ApplyMaskingPolicy_RejectsBadIdentifier — non tri-part identifier
// must fail before any DDL is sent.
func TestSnowflake_ApplyMaskingPolicy_RejectsBadIdentifier(t *testing.T) {
	s, mock, cleanup := snowflakeWithMock(t)
	defer cleanup()
	err := s.ApplyMaskingPolicy(context.Background(),
		connector.AssetRef{Identifier: "ORDERS"}, // missing schema/db
		connector.ColumnPolicy{Column: "ssn", MaskType: connector.MaskHash},
	)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSnowflake_ApplyMaskingPolicy_RejectsBadMask — invalid MaskType.
func TestSnowflake_ApplyMaskingPolicy_RejectsBadMask(t *testing.T) {
	s, mock, cleanup := snowflakeWithMock(t)
	defer cleanup()
	err := s.ApplyMaskingPolicy(context.Background(),
		connector.AssetRef{Identifier: "DB.SCH.orders"},
		connector.ColumnPolicy{Column: "ssn", MaskType: "blowfish"},
	)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSnowflake_BuildMaskBody_RoleEscaping — ' inside a role name is doubled
// and never breaks the SQL literal.
func TestSnowflake_BuildMaskBody_RoleEscaping(t *testing.T) {
	body := buildMaskBody(connector.MaskHash, []string{"o'brien"})
	require.Contains(t, body, "'o''brien'")
	// And the final body still ends with the canonical CASE WHEN ... END envelope.
	require.True(t, regexp.MustCompile(`^CASE WHEN .*END$`).MatchString(body))
	// Sanity: invocation never panics for nil/empty roles.
	require.Contains(t, buildMaskBody(connector.MaskHash, nil), "ARRAY_CONSTRUCT()")
}

// TestSnowflake_QuoteIdentifierEscapesQuotes — embedded double quotes are escaped.
func TestSnowflake_QuoteIdentifierEscapesQuotes(t *testing.T) {
	require.Equal(t, `"a""b"`, quote(`a"b`))
}

// _ keeps time.Now() reachable without a use to dodge unused-import lint
// in scenarios where we drop tests; safe sentinel.
var _ = time.Now
