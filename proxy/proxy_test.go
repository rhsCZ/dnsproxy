package proxy_test

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AdguardTeam/dnsproxy/internal/bootstrap"
	"github.com/AdguardTeam/dnsproxy/internal/dnsproxytest"
	"github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/AdguardTeam/golibs/logutil/slogutil"
	"github.com/AdguardTeam/golibs/testutil"
	"github.com/AdguardTeam/golibs/testutil/servicetest"
	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// defaultTimeout is a default timeout for tests.
	defaultTimeout = 10 * time.Second

	// testTTL is a common time-to-live value in seconds for tests.
	testTTL = 60
)

var (
	// testLogger is a common logger for tests.
	testLogger = slogutil.NewDiscardLogger()

	// testIPv4 is a common IPv4 for tests
	testIPv4 = net.IP{192, 0, 2, 0}
)

// newCustomUpstreamConfig is a helper function that returns an initialized
// [*proxy.CustomUpstreamConfig].
func newCustomUpstreamConfig(ups upstream.Upstream, enabled bool) (c *proxy.CustomUpstreamConfig) {
	return proxy.NewCustomUpstreamConfig(
		&proxy.UpstreamConfig{Upstreams: []upstream.Upstream{ups}},
		enabled,
		0,
		false,
	)
}

// isCachedWithCustomConfig is a helper function that returns the caching
// results of a constructed request using the provided custom upstream
// configuration and FQDN.
func isCachedWithCustomConfig(
	tb testing.TB,
	p *proxy.Proxy,
	conf *proxy.CustomUpstreamConfig,
	fqdn string,
) (isCached bool) {
	tb.Helper()

	d := &proxy.DNSContext{
		CustomUpstreamConfig: conf,
		Req:                  (&dns.Msg{}).SetQuestion(fqdn, dns.TypeA),
	}

	err := p.Resolve(testutil.ContextWithTimeout(tb, defaultTimeout), d)
	require.NoError(tb, err)

	qs := d.QueryStatistics()
	require.NotNil(tb, qs)

	s := qs.Main()
	require.Len(tb, s, 1)

	return s[0].IsCached
}

func TestProxy_Resolve_cache(t *testing.T) {
	const host = "example.test."

	ups := &dnsproxytest.Upstream{
		OnAddress: func() (addr string) { return "stub" },
		OnClose:   func() (err error) { return nil },
	}
	ups.OnExchange = func(req *dns.Msg) (resp *dns.Msg, err error) {
		resp = (&dns.Msg{}).SetReply(req)
		resp.Answer = append(resp.Answer, &dns.A{
			Hdr: dns.RR_Header{
				Name:   req.Question[0].Name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    testTTL,
			},
			A: testIPv4,
		})

		return resp, nil
	}

	upsConf := &proxy.UpstreamConfig{
		Upstreams: []upstream.Upstream{ups},
	}

	testCases := []struct {
		customUpstreamConf *proxy.CustomUpstreamConfig
		wantCachedWithConf assert.BoolAssertionFunc
		wantCachedGlobal   assert.BoolAssertionFunc
		name               string
		prxCacheEnabled    bool
	}{{
		customUpstreamConf: nil,
		wantCachedWithConf: assert.True,
		wantCachedGlobal:   assert.True,
		name:               "global_cache",
		prxCacheEnabled:    true,
	}, {
		customUpstreamConf: newCustomUpstreamConfig(ups, true),
		wantCachedWithConf: assert.True,
		wantCachedGlobal:   assert.False,
		name:               "custom_cache",
		prxCacheEnabled:    false,
	}, {
		customUpstreamConf: newCustomUpstreamConfig(ups, false),
		wantCachedWithConf: assert.False,
		wantCachedGlobal:   assert.False,
		name:               "custom_cache_only_upstreams",
		prxCacheEnabled:    false,
	}, {
		customUpstreamConf: newCustomUpstreamConfig(ups, true),
		wantCachedWithConf: assert.True,
		wantCachedGlobal:   assert.False,
		name:               "two_caches_enabled",
		prxCacheEnabled:    true,
	}, {
		customUpstreamConf: nil,
		wantCachedWithConf: assert.False,
		wantCachedGlobal:   assert.False,
		name:               "proxy_cache_disabled",
		prxCacheEnabled:    false,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := proxy.New(&proxy.Config{
				Logger:         testLogger,
				UDPListenAddr:  []*net.UDPAddr{net.UDPAddrFromAddrPort(localhostAnyPort)},
				UpstreamConfig: upsConf,
				CacheEnabled:   tc.prxCacheEnabled,
			})
			require.NoError(t, err)
			require.NotNil(t, p)

			servicetest.RequireRun(t, p, testTimeout)

			res := isCachedWithCustomConfig(t, p, tc.customUpstreamConf, host)
			assert.False(t, res)

			res = isCachedWithCustomConfig(t, p, tc.customUpstreamConf, host)
			tc.wantCachedWithConf(t, res)

			res = isCachedWithCustomConfig(t, p, nil, host)
			tc.wantCachedGlobal(t, res)
		})
	}
}

func TestProxy_Start_closeOnFail(t *testing.T) {
	t.Parallel()

	l, err := net.ListenTCP(bootstrap.NetworkTCP, net.TCPAddrFromAddrPort(localhostAnyPort))
	require.NoError(t, err)

	tcpAddr := testutil.RequireTypeAssert[*net.TCPAddr](t, l.Addr())

	ups := &dnsproxytest.Upstream{
		OnExchange: func(m *dns.Msg) (_ *dns.Msg, _ error) { panic(testutil.UnexpectedCall(m)) },
		OnAddress:  func() (_ string) { panic(testutil.UnexpectedCall()) },
		OnClose:    func() (_ error) { panic(testutil.UnexpectedCall()) },
	}

	p, err := proxy.New(&proxy.Config{
		Logger: testLogger,
		// Add a free address.
		UDPListenAddr: []*net.UDPAddr{net.UDPAddrFromAddrPort(localhostAnyPort)},
		// Add a bound address.
		TCPListenAddr:  []*net.TCPAddr{tcpAddr},
		UpstreamConfig: &proxy.UpstreamConfig{Upstreams: []upstream.Upstream{ups}},
	})
	require.NoError(t, err)

	require.True(t, t.Run("start_fail", func(t *testing.T) {
		ctx := testutil.ContextWithTimeout(t, testTimeout)
		err = p.Start(ctx)

		var netErr net.Error
		require.ErrorAs(t, err, &netErr)
	}))

	// Don't panic anymore.
	ups.OnClose = func() (err error) { return nil }

	require.True(t, t.Run("restart_success", func(t *testing.T) {
		require.NoError(t, l.Close())

		servicetest.RequireRun(t, p, testTimeout)
	}))
}

func TestProxy_jiggleVulnerability(t *testing.T) {
	t.Parallel()

	ups := &dnsproxytest.Upstream{
		OnAddress: func() (addr string) { return testIPv4.String() },
		OnClose:   func() (err error) { return nil },
	}
	ups.OnExchange = func(req *dns.Msg) (resp *dns.Msg, err error) {
		resp = (&dns.Msg{}).SetReply(req)
		resp.Answer = append(resp.Answer, &dns.A{
			Hdr: dns.RR_Header{
				Name:   req.Question[0].Name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    testTTL,
			},
			A: testIPv4,
		})

		return resp, nil
	}

	upsConf := &proxy.UpstreamConfig{
		Upstreams: []upstream.Upstream{ups},
	}

	// TODO(f.setrakov): !! Improve test case names or consider not using
	// testdata.
	testCases := []string{
		"1004_client_subnet_option_ecs_with_private_ip_but_querying_public_dns",
		"1086_query_for_dnssec_chain_validation_but_cd_1_disables_chain_checking",
		"1166_internal_query_with_ecs_indicating_external_client_subnet",
		"1196_compression_pointer_offset_aligned_to_odd_boundary_causing_misalignment",
		"1214_edns_option_with_option_length_claiming_100_bytes_but_only_10_present",
		"1216_edns_options_list_with_gap_option_1_and_option_3_missing_option_2",
		"1217_edns_option_with_option_length_0_but_option_data_present",
		"1220_edns_ecs_with_source_prefix_length_0_no_client_subnet_info",
		"1221_edns_ecs_with_ipv4_source_prefix_length_33_exceeds_33_bit",
		"1222_edns_ecs_with_ipv6_source_prefix_length_129_exceeds_129_bit",
		"1224_edns_padding_with_non_zero_bytes__should_be_zero_padding",
		"1226_edns_dau_dhu_n3u_options_with_list_length_not_matching_actual_list",
		"1288_rdlength_in_opt_rr_set_to_65535_max_uint16",
		"1294_ecs_source_prefix_length_set_to_255_exceeds_ip_address_bits",
		"1325_edns_padding_option_at_the_beginning_instead_of_end",
		"1326_edns_options_with_duplicate_option_codes",
		"1367_query_with_multiple_edns_options_each_claiming_large_lengths",
		"1370_query_with_opt_rr_header_complete_but_rdata_truncated",
		"1372_query_with_edns_option_option_length_extending_beyond_packet",
		"1435_edns_option_chain_with_backward_option_code_ordering",
		"1436_edns_option_with_option_code_gaps_8_10_12_14_missing_odd_codes",
		"1437_edns_options_with_total_length_exceeding_rdlen",
		"1464_opt_rr_with_name_containing_invalid_label_length",
		"1469_compression_pointer_with_offset_16382__max_valid_14_bit_value",
		"1473_compression_pointer_with_offset_using_only_lower_14_bits",
		"1500_query_with_version_255_and_all_edns_options_present",
		"1520_tcp_query_with_data_beyond_declared_length_prefix",
		"1521_query_with_edns_option_extending_beyond_rdlen_boundary",
		"1531_compression_pointer_in_authority_section_pointing_to_additional_section",
		"1534_compression_chain_spanning_all_four_sections",
		"1607_query_designed_to_fragment_at_compression_pointer_boundary",
		"1620_query_with_ecs_indicating_internal_subnet_from_external_source",
		"332_rr_name_with_compression_pointer_offset_16368_exceeding_message",
		"340_circular_compression_reference_chain_variant_4",
		"349_compression_pointer_chain_with_depth_200",
		"375_opt_rr_with_name_set_to_label_sequence_variant_9",
		"458_edns_with_version_19",
		"504_rdlen_10_actual_options_length_6",
		"513_ecs_option_with_scope_prefix_length_non_zero_in_query",
		"524_ecs_query_with_scope_prefix_length_11",
		"547_ecs_option_with_unsupported_address_family",
		"611_dau_option_with_option_code_8_instead_of_5",
		"625_dhu_option_with_option_code_8_instead_of_6",
		"639_n3u_option_with_option_code_8_instead_of_7",
		"656_ecs_option_with_option_code_11_instead_of_8",
		"670_expire_option_with_option_code_11_instead_of_9",
		"684_cookie_option_with_option_code_11_instead_of_10",
		"696_tcp_keepalive_option_with_option_code_8_instead_of_11",
		"713_padding_option_with_option_code_11_instead_of_12",
		"724_chain_option_with_option_code_8_instead_of_13",
		"738_key_tag_option_with_option_code_8_instead_of_14",
		"753_ede_option_with_option_code_8_instead_of_15",
		"763_expire_query_with_option_length_1_instead_of_0",
		"996_tiny_udp_payload_size_64_declared_but_rdlength_claims_1000_bytes_of_options",
	}

	for _, name := range testCases {
		testDataPath := filepath.Join("testdata", t.Name())

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, err := proxy.New(&proxy.Config{
				UDPListenAddr:  []*net.UDPAddr{net.UDPAddrFromAddrPort(localhostAnyPort)},
				UpstreamConfig: upsConf,
				Logger:         testLogger,
			})
			require.NoError(t, err)
			require.NotNil(t, p)

			servicetest.RequireRun(t, p, testTimeout)

			data, err := os.ReadFile(filepath.Join(testDataPath, name+".bin"))
			require.NoError(t, err)

			addr := p.Addr(proxy.ProtoUDP)
			exchangeData(t, addr.String(), data)
		})
	}
}

// exchangeData sends the provided data to a proxy running on addr and checks
// that the server correctly identifies the packet as invalid without crashing
// or returning an error.
func exchangeData(tb testing.TB, addr string, data []byte) {
	tb.Helper()

	conn := requireDial(tb, addr)
	requireWritePacket(tb, conn, data)
	resp := requireReadPacket(tb, conn)

	msg := &dns.Msg{}
	err := msg.Unpack(resp)
	require.NoError(tb, err)

	assert.Equal(tb, msg.Rcode, dns.RcodeFormatError)
}

// requireDial dials the given address and returns the connection.  The
// connection is closed in the test cleanup.
func requireDial(tb testing.TB, addr string) (conn net.Conn) {
	tb.Helper()

	conn, err := net.DialTimeout(string(proxy.ProtoUDP), addr, testTimeout)
	require.NoError(tb, err)
	testutil.CleanupAndRequireSuccess(tb, conn.Close)

	deadline := time.Now().Add(testTimeout)
	require.NoError(tb, conn.SetDeadline(deadline))

	return conn
}

// requireWritePacket writes data into the given conn.  conn must not be nil.
func requireWritePacket(tb testing.TB, conn net.Conn, data []byte) {
	tb.Helper()

	n, err := conn.Write(data)
	require.NoError(tb, err)
	require.Equal(tb, len(data), n)
}

// requireReadPacket reads a DNS packet from the given conn and returns the
// packet data.  conn must not be nil.
func requireReadPacket(tb testing.TB, conn net.Conn) (data []byte) {
	tb.Helper()

	buf := make([]byte, dns.MaxMsgSize)
	n, err := conn.Read(buf)
	require.NoError(tb, err)
	require.Positive(tb, n)

	return buf[:n]
}
