//go:generate easyjson -all -no_std_marshalers stats.go

/* tunterm/stats --- definition of TTStats struct
 *
 * Copyright 208 MistSys
 */

package ttstats

import (
	"encoding/binary"
	json "encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/mistsys/mist-ap/msgs"
	"github.com/mistsys/mist_go_utils/net/ethernet"
	"github.com/mistsys/mist_go_utils/net/ip"
	"github.com/mistsys/mist_go_utils/uuid"
	"github.com/mistsys/protobuf3/protobuf3"
	"github.com/pkg/errors"
)

// TTStats defines what is published to tt-stats- topics.
// Note: many of the protobuf IDs match the same field in APStats. That is due to lazyness (C&V). It is not a virtue. TTStats and APStats must eventually diverge.
type TTStats struct {
	ID        uuid.UUID `protobuf:"bytes,1"` // ID of tunterm sending this stat
	OrgID     uuid.UUID `protobuf:"bytes,2"` // the organization-id (who owns the tunterm)
	SiteID    uuid.UUID `protobuf:"bytes,4"` // Site ID configured from the cloud for this tunterm
	ClusterID uuid.UUID `protobuf:"bytes,6"` // Cluster ID configured from the cloud for this tunterm

	Uptime          int32   `protobuf:"varint,18"`  // uptime since linux boot, in seconds; varint is ok, it shouldn't be negative
	FirmwareVersion string  `protobuf:"bytes,20"`   // ep firmware version
	ConfigTimestamp float64 `protobuf:"fixed64,60"` // current configuration's Timestamp field (config ID+Timestamp uniquely identify the config)

	When     time.Time `protobuf:"bytes,5"`    // when these stats were gathered
	Interval float64   `protobuf:"fixed64,23"` // the interval between these stats and the previous TTStat from this EP, in seconds. this is redundant but convenient for now. 0 the first time an EP sends stats after starting up

	EthPorts    []EthPortStats    `protobuf:"bytes,25"`
	LAGs        []LAGStats        `protobuf:"bytes,102"`
	SVIs        []SVIStats        `protobuf:"bytes,26"`
	HostPort    HostPortStats     `protobuf:"bytes,100"` // we have just one host port (kni0)
	L2TPTunnels []L2TPTunnelStats `protobuf:"bytes,27"`

	Datapath struct {
		Mempools []MempoolStats `protobuf:"bytes,1"` // state of the datapath memory pools
		LCores   []LCoreStats   `protobuf:"bytes,2"` // stats from each datapath lcore
	} `protobuf:"bytes,101"`

	Cloud CloudStats `protobuf:"bytes,48"`

	// below are fields added by the terminator
	InfoFromTerminator *msgs.InfoFromTerminator `protobuf:"bytes,2047" json:",omitempty"` // data about the message inserted by the terminator. This is more trustworthy than the inner contents of the message because the EP can't control the contents. NOTE ID 2047 matches msgs.InfoFromTerminatorID value
}

// note: protobuf matches with ep.stats.SwitchportStats, on which this is based
type EthPortStats struct {
	Name       string `protobuf:"bytes,1"`  // name of port (eth0)
	Link       bool   `protobuf:"varint,2"` // whether the PHY has link
	FullDuplex bool   `protobuf:"varint,3"` // full duplex
	//AutoNeg    bool  `protobuf:"varint,4"` // whether it's auto negotiated (commented out b/c for the moment we always autonegociate)
	Mbps uint32 `protobuf:"varint,5"` // link speed in units of Mbps (10, 100, 1000, 2500, 5000, 10000 typically)

	MAC   ethernet.MAC `protobuf:"bytes,101"` // all zeroes, or the port's own MAC address
	State PortState    `protobuf:"varint,102"`

	RxBytes uint64 `protobuf:"varint,6"` // rx bytes
	RxPkts  uint64 `protobuf:"varint,7"` // rx packets

	RxErrors        uint64 `json:",omitempty" protobuf:"varint,17"` // rx packets with sort of error
	RxOverrunErrors uint64 `json:",omitempty" protobuf:"varint,25"` // unsufficient rx buffers (can't keep up)

	TxBytes  uint64 `protobuf:"varint,8"` // tx bytes
	TxPkts   uint64 `protobuf:"varint,9"` // tx packets
	TxErrors uint64 `json:",omitempty" protobuf:"varint,11"`

	LACP *LACPPortStats `json:",omitempty" protobuf:"bytes,100"` // optional, since not all ports participate in LACP

	LLDPNeighbors []LLDPNeighbor `json:",omitempty" protobuf:"bytes,56"` // a list, so that just in case we're recieving more than one, or it is changing and the old one is expiring, we report them all
}

// port state enumeration
type PortState int

// implement protobuf3.AsProtobuf3er to describe the enum
func (*PortState) AsProtobuf3() (string, string) {
	return "PortState", `enum PortState {
  DISCARDING = 0;
  BLOCKING = 1;
  FORWARDING = 2;
}`
}

func (ps *PortState) MarshalProtobuf3() ([]byte, error) {
	// enums in protobuf marshal as varint
	n := protobuf3.SizeVarint(uint64(*ps))
	buf := protobuf3.NewBuffer(make([]byte, 0, n))
	buf.EncodeVarint(uint64(*ps))
	return buf.Bytes(), nil
}

func (ps *PortState) UnmarshalProtobuf3(data []byte) error {
	// enums in protobuf marshal as varint
	buf := protobuf3.NewBuffer(data)
	n, err := buf.DecodeVarint()
	if err != nil {
		return errors.Wrap(err, "not a stats.PortState")
	}
	// should we bounds check?
	*ps = PortState(n)
	return nil
}

func (ps *PortState) MarshalJSON() ([]byte, error) {
	// enums in JSON marshal as strings or numbers
	switch *ps {
	case 0:
		return []byte(`"DISCARDING"`), nil
	case 1:
		return []byte(`"BLOCKING"`), nil
	case 2:
		return []byte(`"FORWARDING"`), nil
	default:
		return []byte(strconv.Itoa(int(*ps))), nil
	}
}

func (ps *PortState) UnmarshalJSON(data []byte) error {
	// enums in JSON marshal as strings or numbers
	str := string(data)
	switch str {
	case `"DISCARDING"`:
		*ps = 0
		return nil
	case `"BLOCKING"`:
		*ps = 1
		return nil
	case `"FORWARDING"`:
		*ps = 2
		return nil
	default:
		if n, err := strconv.Atoi(str); err != nil {
			return errors.Wrapf(err, "%q is not a stats.PortState", str)
		} else {
			*ps = PortState(n)
			return nil
		}
	}
}

// per-port LACP stats
type LACPPortStats struct {
	ActorState uint8  `protobuf:"varint,1"`
	OperKey    uint16 `protobuf:"varint,2"`

	PartnerLAGID LAGID `protobuf:"bytes,3"`
	PartnerState uint8 `protobuf:"varint,4"`

	RxState  string `protobuf:"bytes,5"` // 1st letter of state name (they happen to all be unique)
	MuxState string `protobuf:"bytes,6"` // 1st letter of state name (they happen to all be unique)

	LAG string `json:",omitempty" protobuf:"bytes,7"` // the name of the LAG port ("lacp0") to which this external port belongs
}

type LAGID [14]byte

// format the LAGID in the format recommended by IEEE 802.3ad-2000
func (lid LAGID) String() string {
	return fmt.Sprintf("(%02x%02x,%02x-%02x-%02x-%02x-%02x-%02x,%02x%02x,%02x%02x,%02x%02x)",
		lid[0], lid[1], lid[2], lid[3], lid[4], lid[5], lid[6], lid[7], lid[8], lid[9], lid[10], lid[11], lid[12], lid[13])
}

func (lid *LAGID) MarshalJSON() ([]byte, error) {
	str := lid.String()
	data := make([]byte, len(str)+2)
	data[0] = '"'
	copy(data[1:], str)
	data[len(data)-1] = '"'
	return data, nil
}

type LAGStats struct {
	Name     string   `protobuf:"bytes,1"`                   // name of port (lag0)
	Active   []string `protobuf:"bytes,2"`                   // names of active member ports
	Inactive []string `json:",omitempty" protobuf:"bytes,3"` // names of inactive member ports
}

type HostPortStats struct {
	Name string       `protobuf:"bytes,1"` // typically "kni0"
	MAC  ethernet.MAC `protobuf:"bytes,2"` // MAC address bound to kni0 and thus to all the SVIs
}

// information concerning an SVI (switch vlan interface)
type SVIStats struct {
	Dev  string         `protobuf:"bytes,1"`  // net_device name ("kni0.1")
	Vlan uint16         `protobuf:"varint,2"` // numeric vlan id (1)
	IPs  []ip.AddrMask6 `protobuf:"bytes,3"`  // note: can be empty, and can have more than one element, and contains both IPv4 and IPv6
}

// information concerning one L2TP tunnel
// NOTE WELL there can be duplicate ID values with different LocalConnectionID in the same stats structure during tunnel reconfiguration
type L2TPTunnelStats struct {
	ID                    uuid.UUID          `protobuf:"bytes,1"`   // PAPI ID of tunnel
	State                 string             `protobuf:"bytes,2"`   // tunnel state machine state
	LocalAddr             ip.AddrPort6       `protobuf:"bytes,22"`  // local IP and port
	PeerAddr              ip.AddrPort6       `protobuf:"bytes,23"`  // peer IP and port
	RouterID              uint32             `protobuf:"fixed32,4"` // host-endian; configured Router-ID
	Uptime                uint32             `protobuf:"varint,5"`  // seconds since tunnel and session(s) (pseudowires) were all established
	LocalConnectionID     uint32             `protobuf:"fixed32,6"` // host-endian; dynamic tunnel connection id
	RemoteConnectionID    uint32             `protobuf:"fixed32,7"` // host-endian; dynamic tunnel connection id of peer
	TxControlPkts         uint32             `protobuf:"varint,8"`  // count of control packets sent
	RxControlPkts         uint32             `protobuf:"varint,9"`  // count of control packets received
	Sessions              []L2TPSessionStats `protobuf:"bytes,10"`
	OuterPMTU             uint16             `protobuf:"varint,11"` // outer Path-MTU, or 0 if we don't know
	LastStopCCNResult     L2TPResultCode     `protobuf:"bytes,12"`  // RESULT_CODE AVP value from last StopCCN we sent
	LastPeerStopCCNResult L2TPResultCode     `protobuf:"bytes,13"`  // RESULT_CODE AVP value from last StopCCN we received
	PeerMistID            uuid.UUID          `protobuf:"bytes,14"`  // MIST_ID AVP value from last SCCRQ or SCCRP we received
	PeerMistOrgID         uuid.UUID          `protobuf:"bytes,15"`  // MIST_ORG AVP value from last SCCRQ or SCCRP we received
	PeerMistSiteID        uuid.UUID          `protobuf:"bytes,16"`  // MIST_SITE AVP value from last SCCRQ or SCCRP we received

	ListenerID uuid.UUID `protobuf:"bytes,24"`
}

// the value of an L2TP RESULT_CODE AVP
type L2TPResultCode struct {
	Code    uint16    `protobuf:"varint,1"`
	ErrCode uint16    `protobuf:"varint,2"`
	ErrMsg  string    `protobuf:"bytes,3"`
	When    time.Time `protobuf:"bytes,4"`
}

func (rc L2TPResultCode) IsZero() bool {
	return rc.Code == 0 && rc.ErrCode == 0 && rc.ErrMsg == ""
}

func (rc L2TPResultCode) String() string {
	if rc.IsZero() {
		return ""
	}
	if rc.When.IsZero() {
		return fmt.Sprintf("Result Code %d, Error Code %d, Error Msg %q", rc.Code, rc.ErrCode, rc.ErrMsg)
	}
	return fmt.Sprintf("Result Code %d, Error Code %d, Error Msg %q at %v", rc.Code, rc.ErrCode, rc.ErrMsg, rc.When)
}

// information concerning one L2TP session
type L2TPSessionStats struct {
	DPPort          int    `protobuf:"varint,1"`  // dataplane port # (dynamically allocated)
	RemoteEndID     string `protobuf:"bytes,2"`   // configured Remote-End-ID
	State           string `protobuf:"bytes,3"`   // session state machine state
	LocalSessionID  uint32 `protobuf:"fixed32,4"` // host-endian; dynamic session id
	RemoteSessionID uint32 `protobuf:"fixed32,5"` // host-endian; dynamic session id of peer
}

// somewhat equivalent of net.Addr for stats
type Addr struct {
	IP   net.IP `protobuf:"bytes,1"`  // IP4 or IP6 address
	Port uint16 `protobuf:"varint,2"` // port number
}

func (a Addr) String() string {
	if a.Port != 0 {
		return net.JoinHostPort(a.IP.String(), strconv.FormatUint(uint64(a.Port), 10))
	} else {
		return a.IP.String()
	}
}

func (a *Addr) Set(net_addr net.Addr) {
	if net_addr == nil {
		a.IP = nil
		a.Port = 0
		return
	}

	switch aa := net_addr.(type) {
	case *net.TCPAddr:
		a.IP = aa.IP
		a.Port = uint16(aa.Port)
	case *net.UDPAddr:
		a.IP = aa.IP
		a.Port = uint16(aa.Port)
	case *net.IPAddr:
		a.IP = aa.IP
		a.Port = 0
	default:
		// hmmm, what to do? We need to get the developer's attention
		// I know, we'll panic
		panic(fmt.Sprintf("Can't stats.Addr.Set(%T) yet", net_addr))
	}
}

// encode Addr as a string "IP:Port"
func (a *Addr) MarshalJSON() ([]byte, error) {
	txt := net.JoinHostPort(a.IP.String(), strconv.FormatUint(uint64(a.Port), 10))
	b := make([]byte, 1, len(txt)+2)
	b[0] = '"'
	b = append(b, txt...)
	b = append(b, '"')
	return b, nil
}

// decode a "IP:Port" string
func (a *Addr) UnmarshalJSON(b []byte) error {
	n := len(b)
	if n < 2 || b[0] != '"' || b[n-1] != '"' {
		return errors.Errorf("not a stats.Addr: %q", b)
	}

	h, p, err := net.SplitHostPort(string(b[1 : n-1]))
	if err != nil {
		return errors.Wrapf(err, "not a stats.Addr: %q", b)
	}

	ip := net.ParseIP(h)
	if ip == nil {
		return errors.Errorf("not a stats.Addr.IP: %q", b)
	}
	if ip4 := ip.To4(); ip4 != nil { // net.ParseIP() returns IPv4 embedded in IPv6. Weird.
		ip = ip4
	}

	pp, err := strconv.ParseUint(p, 10, 16)
	if err != nil {
		return errors.Wrapf(err, "not a stats.Addr.Port: %q", b)
	}

	a.IP = ip
	a.Port = uint16(pp)

	return nil
}

type MempoolStats struct {
	Name      string `protobuf:"bytes,1"`
	MegaBytes uint64 `protobuf:"varint,2"` // memory used by pool in megabytes
	Entries   uint64 `protobuf:"varint,3"` // total # of entries in the pool
	Free      uint64 `protobuf:"varint,4"` // # of free entries in the pool
}

type LCoreStats struct {
	Core   int    `protobuf:"varint,1"` // core ID number
	RxPkts uint64 `protobuf:"varint,2"` // # of pkts received into tunterm by the core for processing
	TxPkts uint64 `protobuf:"varint,3"` // # of pkts send by the core out of tunterm
}

// useful things we've learned from an LLDP-sending neighbor
type LLDPNeighbor struct {
	SMAC                ethernet.MAC            `protobuf:"bytes,1"`  // source MAC of LLDP packet
	DMAC                ethernet.MAC            `protobuf:"bytes,10"` // destination MAC of LLDP packet
	ChassisID           LLDPChassisID           `protobuf:"bytes,2"`
	PortID              LLDPPortID              `protobuf:"bytes,3"`
	PortDescription     string                  `json:",omitempty" protobuf:"bytes,4"`
	SystemName          string                  `json:",omitempty" protobuf:"bytes,5"`
	SystemDescription   string                  `json:",omitempty" protobuf:"bytes,6"`
	Capabilities        LLDPCapabilities        `json:",omitempty" protobuf:"bytes,7"` // bitfields. See 802.1AB-2016 for more
	ManagementAddresses []LLDPManagementAddress `protobuf:"bytes,8"`
	PVID                *uint16                 `json:",omitempty" protobuf:"varint,9"` // optional, since TLV need not be present in LLDPDU
}

type LLDPChassisID []byte // 1st byte is type of ID

func (id LLDPChassisID) String() string {
	if len(id) < 1 {
		return ""
	}
	switch id[0] { // See table in 802.1AB-2016 for interpretation
	case 4: // MAC
		if len(id) == 1+6 {
			return ethernet.MakeMACFromBytes(id[1:]).String()
		}
	case 5: // network address
		if len(id) >= 2 {
			switch id[1] { // IANA address family number
			case 1: // IPv4
				if len(id) == 2+4 {
					return ip.MakeAddr4FromBytes(id[2:]).String()
				}
			case 2: // IPv6
				if len(id) == 2+16 {
					return ip.MakeAddr6FromBytes(id[2:]).String()
				}
			}
		}
	case 6, 7: // ifName, locally assigned are both strings
		return fmt.Sprintf("%q", []byte(id[1:]))
	}
	// if we get here then the ID is some type we don't print nicely
	return fmt.Sprintf("type %d: % x", id[0], []byte(id[1:]))
}

func (id LLDPChassisID) MarshalJSON() ([]byte, error) {
	// id.String() can have any weird char sent by the peer in it; let json.Marshal deal with it
	s := id.String()
	// note: if we surrounded it with quotes, strip them and let JSON add them back. It makes the JSON much more readable
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	return json.Marshal(s)
}

type LLDPPortID []byte

func (id LLDPPortID) String() string {
	if len(id) < 1 {
		return ""
	}
	switch id[0] { // See table in 802.1AB-2016 for interpretation
	case 3: // MAC
		if len(id) == 1+6 {
			return ethernet.MakeMACFromBytes(id[1:]).String()
		}
	case 4: // network address
		if len(id) >= 2 {
			switch id[1] { // IANA address family number
			case 1: // IPv4
				if len(id) == 2+4 {
					return ip.MakeAddr4FromBytes(id[2:]).String()
				}
			case 2: // IPv6
				if len(id) == 2+16 {
					return ip.MakeAddr6FromBytes(id[2:]).String()
				}
			}
		}
	case 5, 7: // ifName, locally assigned are both strings
		return fmt.Sprintf("%q", []byte(id[1:]))
	}
	// if we get here then the ID is some type we don't print nicely
	return fmt.Sprintf("type %d: % x", id[0], []byte(id[1:]))
}

func (id LLDPPortID) MarshalJSON() ([]byte, error) {
	// id.String() can have any weird char sent by the peer in it; let json.Marshal deal with it
	s := id.String()
	// note: if we surrounded it with quotes, strip them and let JSON add them back. It makes the JSON much more readable
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	return json.Marshal(s)
}

type LLDPCapabilities []byte

func (c LLDPCapabilities) String() string {
	var str string
	if len(c) >= 4 {
		// the first 4 bytes are two 16-bit bitfields
		caps := binary.BigEndian.Uint16(c)
		enabled := binary.BigEndian.Uint16(c[2:])
		str = fmt.Sprintf("%04x/%04x", caps, enabled)
		c = c[4:]
	}
	if len(c) != 0 {
		// append any future bytes as-is
		str += fmt.Sprintf(" % x", []byte(c))
	}
	return str
}

func (c LLDPCapabilities) MarshalJSON() ([]byte, error) {
	str := c.String() // note: we've arranged things so that the string never needs to be escaped in JSON
	b := make([]byte, 1+len(str)+1)
	b[0] = '"'
	copy(b[1:], str)
	b[len(b)-1] = '"'
	return b, nil
}

type LLDPManagementAddress []byte

func (ma LLDPManagementAddress) String() string {
	if len(ma) < 1 {
		return ""
	}
	// 1st byte is the length of the address portion
	n := int(ma[0])
	if len(ma) >= 1+n {
		addr := ma[1 : 1+n]
		// next byte is the address family
		switch addr[0] { // See IANA address family numbering for interpretation. https://www.iana.org/assignments/address-family-numbers/
		case 1: // IPv4
			if len(addr) == 1+4 {
				return ip.MakeAddr4FromBytes(addr[1:]).String()
			}
		case 2: // IPv6
			if len(addr) == 1+16 {
				return ip.MakeAddr6FromBytes(addr[1:]).String()
			}
		}
	}

	// TODO someday if needed, add the interface number and OID to the string

	// if we get here then the address is some type we don't print nicely
	return fmt.Sprintf("% x", []byte(ma))
}

func (ma LLDPManagementAddress) MarshalJSON() ([]byte, error) {
	str := ma.String() // note: we've arranged things so that the string never needs to be escaped in JSON
	b := make([]byte, 1+len(str)+1)
	b[0] = '"'
	copy(b[1:], str)
	b[len(b)-1] = '"'
	return b, nil
}

// state of the cloud connection
type CloudStats struct {
	LastRTT float32 `protobuf:"fixed32,1"` // last RTT measured between TT and cloud, in seconds (hopefully much less than one second). If 0 then it isn't known.
	//WhenLastRTT time.Time `protobuf:"bytes,2"`   // when LastRTT was measured
	PMTU    uint32 `protobuf:"varint,3"` // path-MTU discovered by TCP connection
	SendMSS uint32 `protobuf:"varint,4"` // MSS of TCP connection when sending. When the path includes TCP MSS clamping it happens that the PMTU is never discovered, but the clamped MSS tells the story

	SIP               ip.Addr6 `protobuf:"bytes,5"`  // local SIP of cloud connection
	ReconnectionCount uint     `protobuf:"varint,6"` // 0-based count of how many times this tunterm instance has successfully reconnected to the cloud
}
