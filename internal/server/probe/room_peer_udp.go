package probe

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	roomstate "qqtang/internal/game/room"
	"qqtang/internal/protocol/capture"
	"qqtang/internal/protocol/game"
)

// Mode-0 Type-3 candidates are refreshed every three seconds while QQTPPP is
// negotiating, so a short lease rejects stale NAT mappings. Mode-1 Type-1
// presence is different: the 5.2 client stops sending Type-1 after the server
// returns its observed endpoint. That registration therefore follows the
// authenticated TCP session lifetime and must not be expired by this timer.
const (
	roomPeerUDPMode0EndpointLifetime = 15 * time.Second
)

type roomPeerUDPKey struct {
	RoomID uint16
	UIN    uint32
}

type roomPeerUDPEndpoint struct {
	PlayerID     uint16
	PacketNumber uint32
	Address      *net.UDPAddr
	Connection   *net.UDPConn
	Local        string
	Listener     string
	LastUpdated  time.Time
}

type roomPeerRelayDiagnosticKey struct {
	RoomID         uint16
	SenderUIN      uint32
	TargetPlayerID uint16
}

type roomPeerRelayDiagnostic struct {
	WindowStart time.Time
	Packets     uint64
	Bytes       uint64
}

// handleLegacyUDPControl handles QQTPPP's raw room-peer UDP control protocol.
// It is intentionally separate from the QQT message dispatcher and from
// NetCenter's independently recovered 10-byte relay envelope.
func (server *Server) handleLegacyUDPControl(connection *net.UDPConn, listenerName, connectionID, local string, remote *net.UDPAddr, data []byte) bool {
	if isLegacyUDPPeerHandshakeDatagram(data) {
		handshake, err := game.DecodeLegacyUDPPeerHandshake(data)
		if err != nil {
			server.log(logEvent{
				Level: "warn", Event: "room_peer_udp_handshake_rejected", ConnectionID: connectionID,
				MessageLength: len(data), Result: "invalid_packet", ErrorContext: err.Error(),
				Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
			})
			return true
		}
		server.handleLegacyUDPPeerHandshake(connection, connectionID, local, remote, handshake, data)
		return true
	}
	if !isLegacyUDPControlDatagram(data) {
		return false
	}
	packet, err := game.DecodeLegacyUDPControlPacket(data)
	if err != nil {
		server.log(logEvent{
			Level: "warn", Event: "legacy_udp_control_rejected", ConnectionID: connectionID,
			MessageLength: len(data), Result: "invalid_packet", ErrorContext: err.Error(),
			Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
		})
		return true
	}
	switch packet.Header.Type {
	case game.LegacyUDPPresenceType:
		server.handleLegacyUDPPresence(connection, listenerName, connectionID, local, remote, packet, data)
	case game.LegacyUDPMulticastType:
		server.handleLegacyUDPMulticast(connection, connectionID, local, remote, packet, data)
	case game.LegacyUDPRoomPeerType:
		server.handleLegacyUDPRoomPeer(connection, listenerName, connectionID, local, remote, packet, data)
	}
	return true
}

func isLegacyUDPPeerHandshakeDatagram(data []byte) bool {
	if len(data) != game.LegacyUDPPeerHandshakeSize || int(binary.BigEndian.Uint16(data[0:2])) != len(data) {
		return false
	}
	return data[2] == game.LegacyUDPPeerHandshakeRequest || data[2] == game.LegacyUDPPeerHandshakeResponse
}

// roomPeerMulticastDatagram is a cheap outer-frame classifier used only to
// keep high-FPS Type-2 traffic out of synchronous diagnostics. Authorization
// and target validation still happen in handleLegacyUDPMulticast; the encrypted
// gameplay body is not decoded merely for logging.
func roomPeerMulticastDatagram(data []byte) bool {
	return len(data) >= game.LegacyUDPControlHeaderSize &&
		int(binary.BigEndian.Uint16(data[0:2])) == len(data) &&
		game.LegacyUDPMessageType(binary.BigEndian.Uint16(data[13:15])) == game.LegacyUDPMulticastType
}

// handleLegacyUDPPeerHandshake is a relay fallback for the case where
// QQTPPP selected the rendezvous server as its peer candidate. A native direct
// exchange never reaches this function. The 15-byte frame has no target UIN,
// so forwarding one to a guessed peer would be incorrect; the authenticated
// sender's other current room peers are the protocol-complete target set.
func (server *Server) handleLegacyUDPPeerHandshake(connection *net.UDPConn, connectionID, local string, remote *net.UDPAddr, handshake game.LegacyUDPPeerHandshake, data []byte) {
	if server.handleCompetitiveAIVirtualPeerHandshake(connection, connectionID, local, remote, handshake, data) {
		return
	}
	roomID, targets, err := server.resolveLegacyUDPPeerHandshakeTargets(handshake, remote)
	if err != nil {
		server.log(logEvent{
			Level: "warn", Event: "room_peer_udp_handshake_rejected", ConnectionID: connectionID,
			AccountID: fmt.Sprint(handshake.UIN), MessageLength: len(data), Result: "unauthorized",
			ErrorContext: err.Error(), Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
		})
		return
	}
	for _, target := range targets {
		if _, err = target.Connection.WriteToUDP(data, target.Address); err != nil {
			server.log(logEvent{
				Level: "error", Event: "room_peer_udp_handshake_write_failed", ConnectionID: connectionID,
				AccountID: fmt.Sprint(handshake.UIN), RoomID: fmt.Sprint(roomID), MessageLength: len(data),
				Result: fmt.Sprintf("target_player_%d", target.PlayerID), ErrorContext: err.Error(), Network: "udp",
				LocalAddress: target.Local, RemoteAddress: target.Address.String(),
			})
			continue
		}
		server.captureRecord(capture.NewRecord(time.Now(), connectionID, "server_to_client", "udp", target.Local, target.Address.String(), data))
		server.log(logEvent{
			Level: "info", Event: "room_peer_udp_handshake_routed", ConnectionID: connectionID,
			AccountID: fmt.Sprint(handshake.UIN), RoomID: fmt.Sprint(roomID), MessageLength: len(data),
			Result: fmt.Sprintf("flags_0x%02x_target_player_%d", handshake.Flags, target.PlayerID), Network: "udp",
			LocalAddress: target.Local, RemoteAddress: target.Address.String(),
		})
	}
}

// handleCompetitiveAIVirtualPeerHandshake answers the native 0x20 direct-peer
// probe on behalf of every server-owned peer in the sender's active match. A
// normal client would return the same 0x21 frame itself; retaining this leg is
// what lets QQTPPP advance from candidate discovery to its non-zero 0x0086.
func (server *Server) handleCompetitiveAIVirtualPeerHandshake(connection *net.UDPConn, connectionID, local string, remote *net.UDPAddr, handshake game.LegacyUDPPeerHandshake, data []byte) bool {
	if connection == nil || remote == nil || handshake.Flags != game.LegacyUDPPeerHandshakeRequest {
		return false
	}
	remoteIP := remote.IP.String()
	server.liveMu.RLock()
	var sender *connectionSession
	for _, live := range server.liveSessions {
		if live.liveUIN.Load() == handshake.UIN && remoteHost(live.remoteAddress) == remoteIP {
			sender = live
			break
		}
	}
	server.liveMu.RUnlock()
	if sender == nil || sender.liveRoomID.Load() == 0 {
		return false
	}
	runtime := server.competitiveAIRuntimeForGame(sender.gameplayGameID())
	if runtime == nil || sender.liveRoomID.Load() != uint32(runtime.roomID) {
		return false
	}
	humanPlayerID := sender.routingPlayerID()
	runtime.mu.Lock()
	_, human := runtime.humanIDs[humanPlayerID]
	runtime.mu.Unlock()
	if !human {
		return false
	}

	// Keep the same authenticated mode-0 source requirement used by the real
	// relay path. The 15-byte frame has no target identity of its own.
	key := roomPeerUDPKey{RoomID: runtime.roomID, UIN: handshake.UIN}
	server.roomPeerUDPMu.Lock()
	registered, found := server.roomPeerUDPEndpoints[key]
	validSource := found && registered.PlayerID == humanPlayerID && registered.Address != nil &&
		registered.Address.IP.Equal(remote.IP) && registered.Address.Port == remote.Port
	server.roomPeerUDPMu.Unlock()
	if !validSource {
		return false
	}

	serverIP := net.ParseIP(server.config.NetworkSettings.ServerIP).To4()
	localAddress, ok := connection.LocalAddr().(*net.UDPAddr)
	if serverIP == nil || !ok || localAddress.Port <= 0 || localAddress.Port > 65535 {
		server.log(logEvent{Level: "error", Event: "competitive_ai_peer_handshake_failed", ConnectionID: connectionID, AccountID: fmt.Sprint(handshake.UIN), RoomID: fmt.Sprint(runtime.roomID), MessageLength: len(data), ErrorContext: "authoritative UDP endpoint is unavailable", Network: "udp", LocalAddress: local, RemoteAddress: remote.String()})
		return true
	}
	var endpoint game.LegacyUDPEndpoint
	copy(endpoint.IPv4[:], serverIP)
	endpoint.Port = uint16(localAddress.Port)
	for _, projection := range runtime.roomProjections {
		response, err := (game.LegacyUDPPeerHandshake{Flags: game.LegacyUDPPeerHandshakeResponse, UIN: projection.UIN, Endpoint: endpoint}).Encode()
		if err == nil {
			_, err = connection.WriteToUDP(response, remote)
		}
		if err != nil {
			server.log(logEvent{Level: "error", Event: "competitive_ai_peer_handshake_failed", ConnectionID: connectionID, AccountID: fmt.Sprint(handshake.UIN), RoomID: fmt.Sprint(runtime.roomID), MessageLength: len(data), Result: fmt.Sprintf("virtual_player_%d", projection.PlayerID), ErrorContext: err.Error(), Network: "udp", LocalAddress: local, RemoteAddress: remote.String()})
			continue
		}
		server.captureRecord(capture.NewRecord(time.Now(), connectionID, "server_to_client", "udp", local, remote.String(), response))
		server.log(logEvent{Level: "info", Event: "competitive_ai_peer_handshake_ready", ConnectionID: connectionID, AccountID: fmt.Sprint(handshake.UIN), RoomID: fmt.Sprint(runtime.roomID), MessageLength: len(response), Result: fmt.Sprintf("virtual_player_%d", projection.PlayerID), Network: "udp", LocalAddress: local, RemoteAddress: remote.String()})
	}
	return true
}

func (server *Server) resolveLegacyUDPPeerHandshakeTargets(handshake game.LegacyUDPPeerHandshake, remote *net.UDPAddr) (uint16, []roomPeerUDPEndpoint, error) {
	if remote == nil {
		return 0, nil, fmt.Errorf("observed endpoint is absent")
	}
	remoteIP := remote.IP.String()
	server.liveMu.RLock()
	var sender *connectionSession
	for _, live := range server.liveSessions {
		if live.liveUIN.Load() == handshake.UIN && remoteHost(live.remoteAddress) == remoteIP {
			sender = live
			break
		}
	}
	server.liveMu.RUnlock()
	if sender == nil {
		return 0, nil, fmt.Errorf("sender UIN %d has no live session on host %s", handshake.UIN, remoteIP)
	}
	senderPlayerID := sender.routingPlayerID()
	roomID := uint16(sender.liveRoomID.Load())
	if roomID == 0 {
		return 0, nil, fmt.Errorf("sender UIN %d is not in an active room", handshake.UIN)
	}
	state, active := server.worldState().Room(roomID)
	if !active {
		return 0, nil, fmt.Errorf("room %d is not active", roomID)
	}
	snapshot := state.Snapshot()
	if !snapshotHasRoomPeer(snapshot, senderPlayerID) {
		return 0, nil, fmt.Errorf("sender UIN %d is absent from room %d membership", handshake.UIN, roomID)
	}

	now := time.Now()
	senderKey := roomPeerUDPKey{RoomID: roomID, UIN: handshake.UIN}
	server.roomPeerUDPMu.Lock()
	defer server.roomPeerUDPMu.Unlock()
	server.pruneRoomPeerUDPEndpointsLocked(now)
	registered, found := server.roomPeerUDPEndpoints[senderKey]
	if !found || registered.PlayerID != senderPlayerID || registered.Address == nil || !registered.Address.IP.Equal(remote.IP) || registered.Address.Port != remote.Port {
		return 0, nil, fmt.Errorf("sender UIN %d endpoint %s has no current mode-0 registration", handshake.UIN, remote)
	}
	targets := make([]roomPeerUDPEndpoint, 0, game.LegacyUDPMaxTargets-1)
	for key, endpoint := range server.roomPeerUDPEndpoints {
		if key.RoomID != roomID || key.UIN == handshake.UIN || endpoint.Address == nil || endpoint.Connection == nil {
			continue
		}
		if !snapshotHasRoomPeer(snapshot, endpoint.PlayerID) {
			continue
		}
		targets = append(targets, endpoint)
	}
	if len(targets) == 0 {
		return 0, nil, fmt.Errorf("room %d has no other current mode-0 endpoints", roomID)
	}
	return roomID, targets, nil
}

func isLegacyUDPControlDatagram(data []byte) bool {
	if len(data) < game.LegacyUDPControlHeaderSize || len(data) > game.LegacyUDPMaxDatagramSize {
		return false
	}
	if int(binary.BigEndian.Uint16(data[0:2])) != len(data) {
		return false
	}
	switch game.LegacyUDPMessageType(binary.BigEndian.Uint16(data[13:15])) {
	case game.LegacyUDPPresenceType, game.LegacyUDPMulticastType, game.LegacyUDPRoomPeerType:
		return true
	default:
		return false
	}
}

func (server *Server) handleLegacyUDPPresence(connection *net.UDPConn, listenerName, connectionID, local string, remote *net.UDPAddr, packet game.LegacyUDPControlPacket, data []byte) {
	server.touchAuthenticatedSession(remote.String(), packet.Header.UIN)
	if _, err := server.validateLegacyUDPSender(packet.Header, remote); err != nil {
		server.log(logEvent{
			Level: "info", Event: "legacy_udp_presence", ConnectionID: connectionID,
			AccountID: fmt.Sprint(packet.Header.UIN), MessageLength: len(data), Result: "lease_touched_unregistered",
			ErrorContext: err.Error(), Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
		})
		return
	}

	now := time.Now()
	server.roomPeerUDPMu.Lock()
	if server.roomPeerUDPPresence == nil {
		server.roomPeerUDPPresence = make(map[uint32]roomPeerUDPEndpoint)
	}
	server.pruneRoomPeerUDPEndpointsLocked(now)
	packetNumber := packet.Header.PacketNumber
	if previous, found := server.roomPeerUDPPresence[packet.Header.UIN]; found && legacyUDPSequenceAfter(previous.PacketNumber, packetNumber) {
		// TCP fast-path relays borrow this mode-1 sequence space. Do not
		// regress its high-water mark when the client's next periodic Type-1
		// presence was allocated before the relay.
		packetNumber = previous.PacketNumber
	}
	server.roomPeerUDPPresence[packet.Header.UIN] = roomPeerUDPEndpoint{
		PlayerID: packet.Header.PlayerID, PacketNumber: packetNumber,
		Address: cloneUDPAddr(remote), Connection: connection,
		Local: local, Listener: listenerName, LastUpdated: now,
	}
	server.roomPeerUDPMu.Unlock()
	server.log(logEvent{
		Level: "info", Event: "legacy_udp_presence", ConnectionID: connectionID,
		AccountID: fmt.Sprint(packet.Header.UIN), MessageLength: len(data), Result: "presence_registered",
		Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
	})
	server.replyLegacyUDPObservedEndpoint(connection, connectionID, local, remote, packet)
}

// replyLegacyUDPObservedEndpoint completes QQTPPP Type-1 rendezvous. The
// client advertises its local endpoint in the request; the server returns the
// source endpoint observed by this listener. QQTPPP accepts the response only
// from its configured server IPv4 and stores the returned IPv4 in the main
// interface field previously misidentified as a ready/result flag.
func (server *Server) replyLegacyUDPObservedEndpoint(connection *net.UDPConn, connectionID, local string, remote *net.UDPAddr, request game.LegacyUDPControlPacket) {
	observed, err := observedLegacyUDPEndpoint(remote)
	if err != nil {
		server.log(logEvent{
			Level: "warn", Event: "legacy_udp_presence_response_failed", ConnectionID: connectionID,
			AccountID: fmt.Sprint(request.Header.UIN), Result: "invalid_observed_endpoint", ErrorContext: err.Error(),
			Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
		})
		return
	}
	responsePacket := request
	responsePacket.Presence = &observed
	response, err := responsePacket.Encode()
	if err != nil {
		server.log(logEvent{
			Level: "error", Event: "legacy_udp_presence_response_failed", ConnectionID: connectionID,
			AccountID: fmt.Sprint(request.Header.UIN), Result: "encode_failed", ErrorContext: err.Error(),
			Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
		})
		return
	}
	if _, err = connection.WriteToUDP(response, remote); err != nil {
		server.log(logEvent{
			Level: "error", Event: "legacy_udp_presence_response_failed", ConnectionID: connectionID,
			AccountID: fmt.Sprint(request.Header.UIN), MessageLength: len(response), Result: "write_failed", ErrorContext: err.Error(),
			Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
		})
		return
	}
	server.captureRecord(capture.NewRecord(time.Now(), connectionID, "server_to_client", "udp", local, remote.String(), response))
	server.log(logEvent{
		Level: "info", Event: "legacy_udp_presence_observed", ConnectionID: connectionID,
		AccountID: fmt.Sprint(request.Header.UIN), MessageLength: len(response),
		Result:  fmt.Sprintf("observed_%v:%d", observed.IPv4, observed.Port),
		Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
	})
}

// nextRoomFastUDPPacketNumber continues the sender's native mode-1 packet
// sequence. QQTPPP rejects a Type-2 multicast whose number is older than the
// Type-1 presence high-water mark for that peer; an unrelated server counter
// therefore makes correctly delivered movement packets invisible.
func (server *Server) nextRoomFastUDPPacketNumber(senderUIN uint32) (uint32, error) {
	server.roomPeerUDPMu.Lock()
	defer server.roomPeerUDPMu.Unlock()
	presence, found := server.roomPeerUDPPresence[senderUIN]
	if !found || presence.Address == nil || presence.Connection == nil {
		return 0, fmt.Errorf("sender UIN %d has no current mode-1 presence", senderUIN)
	}
	presence.PacketNumber++
	server.roomPeerUDPPresence[senderUIN] = presence
	return presence.PacketNumber, nil
}

func (server *Server) reserveVirtualRoomPeerPacketNumbers(senderUIN uint32, count int) uint32 {
	if senderUIN == 0 || count <= 0 {
		return 0
	}
	server.roomPeerUDPMu.Lock()
	defer server.roomPeerUDPMu.Unlock()
	if server.virtualRoomPeerSeq == nil {
		server.virtualRoomPeerSeq = make(map[uint32]uint32)
	}
	first := uint32(0)
	for range count {
		server.virtualRoomPeerSeq[senderUIN]++
		if server.virtualRoomPeerSeq[senderUIN] == 0 {
			server.virtualRoomPeerSeq[senderUIN] = 1
		}
		if first == 0 {
			first = server.virtualRoomPeerSeq[senderUIN]
		}
	}
	return first
}

// virtualRoomPeerSenderMutex returns the lifetime lock for one server-owned
// room peer. Sequence allocation alone is not enough: movement heartbeats and
// arbitrator requests are produced by different goroutines, so a later packet
// can otherwise reach WriteToUDP before the earlier packet. QQTPPP keeps a
// per-peer receive high-water mark and legitimately drops that late arrival.
//
// The lock intentionally has the same lifetime as virtualRoomPeerSeq. The
// virtual UIN is stable and its sequence also persists across matches because
// the original client may retain that peer's high-water mark.
func (server *Server) virtualRoomPeerSenderMutex(senderUIN uint32) *sync.Mutex {
	server.roomPeerUDPMu.Lock()
	defer server.roomPeerUDPMu.Unlock()
	if server.virtualRoomPeerSendMu == nil {
		server.virtualRoomPeerSendMu = make(map[uint32]*sync.Mutex)
	}
	mutex := server.virtualRoomPeerSendMu[senderUIN]
	if mutex == nil {
		mutex = &sync.Mutex{}
		server.virtualRoomPeerSendMu[senderUIN] = mutex
	}
	return mutex
}

func legacyUDPSequenceAfter(candidate, reference uint32) bool {
	return int32(candidate-reference) > 0
}

func (server *Server) handleLegacyUDPMulticast(connection *net.UDPConn, connectionID, local string, remote *net.UDPAddr, packet game.LegacyUDPControlPacket, data []byte) {
	roomID, sender, targets, virtualTargets, err := server.resolveLegacyUDPMulticastTargets(packet, remote)
	if err != nil {
		server.log(logEvent{
			Level: "warn", Event: "room_peer_udp_multicast_rejected", ConnectionID: connectionID,
			AccountID: fmt.Sprint(packet.Header.UIN), MessageLength: len(data), Result: "unauthorized",
			ErrorContext: err.Error(), Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
		})
		return
	}
	// Scene messages are an ordered QQTPPP stream, not independently movable
	// schema notifications. In particular, 0x0FAE reconciles a pending bird
	// dispatch created by an earlier Type-2 fact, while 0x0FAD consumes an object
	// installed by that stream. Relay the authenticated ciphertext unchanged so
	// packet number, batch indexes, message order and channel all remain native.
	server.relayLegacyUDPMulticastPayload(connection, connectionID, local, packet.Header.UIN, roomID, data, targets, "current")
	if virtualTargets == 0 {
		return
	}
	// A real-client room is a byte-transparent relay and returns above without
	// opening the gameplay body. Only a packet explicitly addressed to a current
	// server-owned AI requires semantic decoding for the Go battle engine.
	decodedBatch, decodeErr := game.DecodeQQTPPPGameplayBatch(packet.Multicast.Data)
	if decodeErr != nil {
		server.log(logEvent{
			Level: "warn", Event: "competitive_ai_room_peer_batch_rejected", ConnectionID: connectionID,
			AccountID: fmt.Sprint(packet.Header.UIN), RoomID: fmt.Sprint(roomID), Result: "decode_failed",
			ErrorContext: decodeErr.Error(),
			Network:      "udp", LocalAddress: local, RemoteAddress: remote.String(),
		})
		return
	}
	if sender == nil || decodedBatch.GameID != sender.gameplayGameID() {
		server.log(logEvent{
			Level: "warn", Event: "competitive_ai_room_peer_batch_rejected", ConnectionID: connectionID,
			AccountID: fmt.Sprint(packet.Header.UIN), RoomID: fmt.Sprint(roomID), Result: "game_mismatch",
			Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
		})
		return
	}
	aiPackages, aiBatchErr := competitiveAIHumanPackagesFromBatch(decodedBatch, packet.Header.PlayerID)
	if aiBatchErr != nil {
		server.log(logEvent{
			Level: "warn", Event: "competitive_ai_room_peer_batch_rejected", ConnectionID: connectionID,
			AccountID: fmt.Sprint(packet.Header.UIN), RoomID: fmt.Sprint(roomID), Result: "identity_mismatch",
			ErrorContext: aiBatchErr.Error(), Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
		})
		return
	}
	server.recordCompetitiveAIHumanUDPPackages(decodedBatch.GameID, packet.Header.PlayerID, packet.Header.PacketNumber, aiPackages)
}

// Only the server-owned recipient mirror applies this receive window. Human
// recipients still receive the original ciphertext and own their receive state.
func (server *Server) recordCompetitiveAIHumanUDPPackages(gameID uint32, senderID uint16, number uint32, packages []game.GameplayDataPackage) {
	runtime := server.competitiveAIRuntimeForGame(gameID)
	if runtime == nil {
		return
	}
	err := runRoomActor(server, runtime.roomID, "competitive-ai-native-udp", func() error {
		if runtime.acceptNativeUDPPacket(senderID, number) {
			server.recordCompetitiveAIHumanPackagesOnRoomActor(runtime, packages)
		}
		return nil
	})
	if err != nil {
		server.log(logEvent{Level: "error", Event: "competitive_ai_native_udp_failed", RoomID: fmt.Sprint(runtime.roomID), ErrorContext: err.Error()})
	}
}

func (runtime *liveCompetitiveAIRuntime) acceptNativeUDPPacket(senderID uint16, number uint32) bool {
	runtime.inboundMu.Lock()
	defer runtime.inboundMu.Unlock()
	if runtime.nativeUDPSequence == nil {
		runtime.nativeUDPSequence = make(map[uint16]uint32)
	}
	if previous, seen := runtime.nativeUDPSequence[senderID]; seen && !legacyUDPSequenceAfter(number, previous) {
		return false
	}
	runtime.nativeUDPSequence[senderID] = number
	return true
}

func competitiveAIHumanPackagesFromBatch(batch game.GameplayBatch, senderPlayerID uint16) ([]game.GameplayDataPackage, error) {
	if batch.GameID == 0 || len(batch.Entries) == 0 {
		return nil, fmt.Errorf("gameplay batch identity is incomplete")
	}
	packages := make([]game.GameplayDataPackage, 0, len(batch.Entries))
	for index, entry := range batch.Entries {
		if entry.Package.GameID != batch.GameID {
			return nil, fmt.Errorf("gameplay batch entry %d game %d does not match %d", index, entry.Package.GameID, batch.GameID)
		}
		if entry.Package.PlayerID != senderPlayerID {
			return nil, fmt.Errorf("gameplay batch entry %d player %d does not match sender %d", index, entry.Package.PlayerID, senderPlayerID)
		}
		packages = append(packages, entry.Package)
	}
	return packages, nil
}

func (server *Server) relayLegacyUDPMulticastPayload(connection *net.UDPConn, connectionID, local string, senderUIN uint32, roomID uint16, data []byte, targets []roomPeerUDPEndpoint, relayKind string) map[uint16]struct{} {
	delivered := make(map[uint16]struct{}, len(targets))
	for _, target := range targets {
		// Relay the authenticated original ciphertext byte-for-byte. The decoded
		// batch above is classification evidence only; rewriting it would require
		// re-encryption and could alter native sequence/acknowledgement semantics.
		if _, err := connection.WriteToUDP(data, target.Address); err != nil {
			server.log(logEvent{
				Level: "error", Event: "room_peer_udp_multicast_write_failed", ConnectionID: connectionID,
				AccountID: fmt.Sprint(senderUIN), RoomID: fmt.Sprint(roomID), MessageLength: len(data),
				Result: fmt.Sprintf("%s_target_player_%d", relayKind, target.PlayerID), ErrorContext: err.Error(), Network: "udp",
				LocalAddress: local, RemoteAddress: target.Address.String(),
			})
			continue
		}
		delivered[target.PlayerID] = struct{}{}
		packets, bytes, report := server.recordRoomPeerRelayDiagnostic(roomID, senderUIN, target.PlayerID, len(data), time.Now())
		if report {
			// Retain one raw sample per route/second. This is diagnostic sampling
			// only; every authenticated datagram above has already been forwarded.
			server.captureRecord(capture.NewRecord(time.Now(), connectionID, "server_to_client", "udp", local, target.Address.String(), data))
			server.log(logEvent{
				Level: "info", Event: "room_peer_udp_multicast_routed_summary", ConnectionID: connectionID,
				AccountID: fmt.Sprint(senderUIN), RoomID: fmt.Sprint(roomID), MessageLength: len(data),
				Result: fmt.Sprintf("target_player_%d_packets_%d_bytes_%d", target.PlayerID, packets, bytes), Network: "udp",
				LocalAddress: local, RemoteAddress: target.Address.String(),
			})
		}
	}
	return delivered
}

func (server *Server) recordRoomPeerRelayDiagnostic(roomID uint16, senderUIN uint32, targetPlayerID uint16, packetBytes int, now time.Time) (packets, bytes uint64, report bool) {
	key := roomPeerRelayDiagnosticKey{RoomID: roomID, SenderUIN: senderUIN, TargetPlayerID: targetPlayerID}
	server.roomPeerUDPMu.Lock()
	if server.roomPeerRelayStats == nil {
		server.roomPeerRelayStats = make(map[roomPeerRelayDiagnosticKey]roomPeerRelayDiagnostic)
	}
	stat := server.roomPeerRelayStats[key]
	if stat.WindowStart.IsZero() {
		stat.WindowStart = now
	}
	stat.Packets++
	stat.Bytes += uint64(packetBytes)
	if now.Sub(stat.WindowStart) >= time.Second {
		packets, bytes, report = stat.Packets, stat.Bytes, true
		stat.WindowStart, stat.Packets, stat.Bytes = now, 0, 0
	}
	server.roomPeerRelayStats[key] = stat
	server.roomPeerUDPMu.Unlock()
	return packets, bytes, report
}

func (server *Server) handleLegacyUDPRoomPeer(connection *net.UDPConn, listenerName, connectionID, local string, remote *net.UDPAddr, packet game.LegacyUDPControlPacket, data []byte) {
	if server.handleCompetitiveAIVirtualRoomPeer(connection, listenerName, connectionID, local, remote, packet, data) {
		return
	}

	validation, err := server.validateRoomPeerUDPSender(packet, remote)
	if err != nil {
		server.log(logEvent{
			Level: "warn", Event: "room_peer_udp_rejected", ConnectionID: connectionID,
			AccountID: fmt.Sprint(packet.Header.UIN), MessageLength: len(data),
			Result: "unauthorized", ErrorContext: err.Error(), Network: "udp",
			LocalAddress: local, RemoteAddress: remote.String(),
		})
		return
	}
	roomID := validation.RoomID

	now := time.Now()
	key := roomPeerUDPKey{RoomID: roomID, UIN: packet.Header.UIN}
	targetKey := roomPeerUDPKey{RoomID: roomID, UIN: packet.RoomPeer.TargetUIN}
	server.roomPeerUDPMu.Lock()
	if server.roomPeerUDPEndpoints == nil {
		server.roomPeerUDPEndpoints = make(map[roomPeerUDPKey]roomPeerUDPEndpoint)
	}
	server.pruneRoomPeerUDPEndpointsLocked(now)
	server.roomPeerUDPEndpoints[key] = roomPeerUDPEndpoint{
		PlayerID: packet.Header.PlayerID, Address: cloneUDPAddr(remote), Connection: connection,
		Local: local, Listener: listenerName, LastUpdated: now,
	}
	target, found := server.roomPeerUDPEndpoints[targetKey]
	server.roomPeerUDPMu.Unlock()

	if !found || target.PlayerID != packet.RoomPeer.TargetPlayerID || target.Connection == nil || target.Address == nil {
		server.log(logEvent{
			Level: "info", Event: "room_peer_udp_registered", ConnectionID: connectionID,
			AccountID: fmt.Sprint(packet.Header.UIN), RoomID: fmt.Sprint(roomID),
			MessageLength: len(data), Result: fmt.Sprintf("target_%d_pending", packet.RoomPeer.TargetUIN),
			Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
		})
		return
	}

	observed, err := observedLegacyUDPEndpoint(remote)
	if err != nil {
		server.log(logEvent{
			Level: "warn", Event: "room_peer_udp_rejected", ConnectionID: connectionID,
			AccountID: fmt.Sprint(packet.Header.UIN), RoomID: fmt.Sprint(roomID),
			MessageLength: len(data), Result: "invalid_observed_endpoint", ErrorContext: err.Error(),
			Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
		})
		return
	}
	// The payload advertises a candidate for the outer-header sender. Replace
	// that untrusted value with the authenticated source observed by this UDP
	// listener before the target installs it as a remote-peer candidate.
	sourceCandidate := observed
	routedPacket := packet
	routedRoomPeer := *packet.RoomPeer
	routedRoomPeer.Endpoint = sourceCandidate
	routedPacket.RoomPeer = &routedRoomPeer
	routedData, err := routedPacket.Encode()
	if err != nil {
		server.log(logEvent{
			Level: "error", Event: "room_peer_udp_encode_failed", ConnectionID: connectionID,
			AccountID: fmt.Sprint(packet.Header.UIN), RoomID: fmt.Sprint(roomID),
			MessageLength: len(data), Result: fmt.Sprintf("target_%d", packet.RoomPeer.TargetUIN),
			ErrorContext: err.Error(), Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
		})
		return
	}
	if _, err = target.Connection.WriteToUDP(routedData, target.Address); err != nil {
		server.log(logEvent{
			Level: "error", Event: "room_peer_udp_write_failed", ConnectionID: connectionID,
			AccountID: fmt.Sprint(packet.Header.UIN), RoomID: fmt.Sprint(roomID),
			MessageLength: len(routedData), Result: fmt.Sprintf("target_%d", packet.RoomPeer.TargetUIN),
			ErrorContext: err.Error(), Network: "udp", LocalAddress: target.Local, RemoteAddress: target.Address.String(),
		})
		return
	}
	server.captureRecord(capture.NewRecord(time.Now(), connectionID, "server_to_client", "udp", target.Local, target.Address.String(), routedData))
	server.log(logEvent{
		Level: "info", Event: "room_peer_udp_routed", ConnectionID: connectionID,
		AccountID: fmt.Sprint(packet.Header.UIN), RoomID: fmt.Sprint(roomID),
		MessageLength: len(routedData), Result: fmt.Sprintf(
			"target_%d_player_%d_candidate_%v:%d_observed_%s",
			packet.RoomPeer.TargetUIN, target.PlayerID, sourceCandidate.IPv4, sourceCandidate.Port, remote.String(),
		),
		Network: "udp", LocalAddress: target.Local, RemoteAddress: target.Address.String(),
	})
}

// handleCompetitiveAIVirtualRoomPeer completes the native Type-3 rendezvous
// for a server-owned actor. Without this response QQTPPP knows the AI's room
// identity but never installs a UDP route for it, so correctly encoded Type-2
// movement is ignored. The advertised endpoint is the same authoritative UDP
// listener that already relays every room packet; no peer process is created.
func (server *Server) handleCompetitiveAIVirtualRoomPeer(connection *net.UDPConn, listenerName, connectionID, local string, remote *net.UDPAddr, packet game.LegacyUDPControlPacket, data []byte) bool {
	if packet.RoomPeer == nil || connection == nil || remote == nil {
		return false
	}
	sender, err := server.validateLegacyUDPSender(packet.Header, remote)
	if err != nil || sender.CurrentGameID == 0 {
		return false
	}
	runtime := server.competitiveAIRuntimeForGame(sender.CurrentGameID)
	if runtime == nil {
		return false
	}
	projection, virtual := runtime.virtualProjection(packet.RoomPeer.TargetPlayerID, packet.RoomPeer.TargetUIN)
	if !virtual {
		return false
	}
	runtime.mu.Lock()
	_, human := runtime.humanIDs[packet.Header.PlayerID]
	runtime.mu.Unlock()
	if !human || sender.liveRoomID.Load() != uint32(runtime.roomID) || sender.gameplayGameID() != runtime.gameID {
		server.log(logEvent{
			Level: "warn", Event: "competitive_ai_room_peer_rejected", ConnectionID: connectionID,
			AccountID: fmt.Sprint(packet.Header.UIN), RoomID: fmt.Sprint(runtime.roomID),
			MessageLength: len(data), Result: fmt.Sprintf("target_player_%d", projection.PlayerID),
			ErrorContext: "sender is not an active human participant", Network: "udp",
			LocalAddress: local, RemoteAddress: remote.String(),
		})
		return true
	}
	// A real peer's Type-3 request is registered before it is forwarded.  The
	// virtual path must preserve that ordering too: the following native 0x20
	// handshake carries no target identity and authenticates its source against
	// this mode-0 endpoint table.
	now := time.Now()
	key := roomPeerUDPKey{RoomID: runtime.roomID, UIN: packet.Header.UIN}
	server.roomPeerUDPMu.Lock()
	if server.roomPeerUDPEndpoints == nil {
		server.roomPeerUDPEndpoints = make(map[roomPeerUDPKey]roomPeerUDPEndpoint)
	}
	server.pruneRoomPeerUDPEndpointsLocked(now)
	server.roomPeerUDPEndpoints[key] = roomPeerUDPEndpoint{
		PlayerID: packet.Header.PlayerID, PacketNumber: packet.Header.PacketNumber,
		Address: cloneUDPAddr(remote), Connection: connection, Local: local,
		Listener: listenerName, LastUpdated: now,
	}
	server.roomPeerUDPMu.Unlock()
	serverIP := net.ParseIP(server.config.NetworkSettings.ServerIP).To4()
	localAddress, ok := connection.LocalAddr().(*net.UDPAddr)
	if serverIP == nil || !ok || localAddress.Port <= 0 || localAddress.Port > 65535 {
		server.log(logEvent{
			Level: "error", Event: "competitive_ai_room_peer_failed", ConnectionID: connectionID,
			AccountID: fmt.Sprint(packet.Header.UIN), RoomID: fmt.Sprint(runtime.roomID),
			MessageLength: len(data), Result: fmt.Sprintf("target_player_%d", projection.PlayerID),
			ErrorContext: "authoritative UDP endpoint is unavailable", Network: "udp",
			LocalAddress: local, RemoteAddress: remote.String(),
		})
		return true
	}
	var endpoint game.LegacyUDPEndpoint
	copy(endpoint.IPv4[:], serverIP)
	endpoint.Port = uint16(localAddress.Port)
	response := game.LegacyUDPControlPacket{
		Header: game.LegacyUDPControlHeader{
			// QQTPPP maintains an independent sequence for Type-3
			// rendezvous. It must not share the high-frequency Type-2
			// gameplay counter. A virtual peer has no independent probe
			// timer, so mirror this human/AI pair's request sequence.
			Flags: packet.Header.Flags, PacketNumber: packet.Header.PacketNumber,
			PlayerID: projection.PlayerID, UIN: projection.UIN, Type: game.LegacyUDPRoomPeerType,
		},
		RoomPeer: &game.LegacyUDPRoomPeerPayload{
			TargetPlayerID: packet.Header.PlayerID, TargetUIN: packet.Header.UIN, Endpoint: endpoint,
		},
	}
	encoded, err := response.Encode()
	if err == nil {
		_, err = connection.WriteToUDP(encoded, remote)
	}
	if err != nil {
		server.log(logEvent{
			Level: "error", Event: "competitive_ai_room_peer_failed", ConnectionID: connectionID,
			AccountID: fmt.Sprint(packet.Header.UIN), RoomID: fmt.Sprint(runtime.roomID),
			MessageLength: len(data), Result: fmt.Sprintf("target_player_%d", projection.PlayerID),
			ErrorContext: err.Error(), Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
		})
		return true
	}
	server.captureRecord(capture.NewRecord(time.Now(), connectionID, "server_to_client", "udp", local, remote.String(), encoded))
	// The authenticated Type-3 request plus a successful same-socket response is
	// the native proof that this human/virtual pair has an installed UDP route.
	// The client does not necessarily emit a later non-zero reliable 0x0086 for
	// a server-owned peer; waiting exclusively for that optional mirror leaves
	// the AI runtime at ready_0_of_N forever even though QQTPPP is already
	// refreshing this route. The remaining native scene countdown still keeps
	// gameplay projection from racing ahead of GAME_BEGIN.
	server.markCompetitiveAINativePeerReady(runtime, packet.Header.PlayerID, projection.PlayerID)
	server.log(logEvent{
		Level: "info", Event: "competitive_ai_room_peer_ready", ConnectionID: connectionID,
		AccountID: fmt.Sprint(packet.Header.UIN), RoomID: fmt.Sprint(runtime.roomID),
		MessageLength: len(encoded), Result: fmt.Sprintf("virtual_player_%d_uin_%d", projection.PlayerID, projection.UIN),
		Network: "udp", LocalAddress: local, RemoteAddress: remote.String(),
	})
	return true
}

func (server *Server) resolveLegacyUDPMulticastTargets(packet game.LegacyUDPControlPacket, remote *net.UDPAddr) (uint16, *connectionSession, []roomPeerUDPEndpoint, int, error) {
	if packet.Multicast == nil || remote == nil {
		return 0, nil, nil, 0, fmt.Errorf("multicast packet or observed endpoint is absent")
	}
	sender, err := server.validateLegacyUDPSender(packet.Header, remote)
	if err != nil {
		return 0, nil, nil, 0, err
	}
	roomID := uint16(sender.liveRoomID.Load())
	if roomID == 0 {
		return 0, nil, nil, 0, fmt.Errorf("sender is not in an active room")
	}
	state, active := server.worldState().Room(roomID)
	if !active {
		return 0, nil, nil, 0, fmt.Errorf("room %d is not active", roomID)
	}
	snapshot := state.Snapshot()
	if !snapshotHasRoomPeer(snapshot, packet.Header.PlayerID) {
		return 0, nil, nil, 0, fmt.Errorf("sender is absent from room %d membership", roomID)
	}

	now := time.Now()
	observedSource, err := observedLegacyUDPEndpoint(remote)
	if err != nil {
		return 0, nil, nil, 0, err
	}
	var aiRuntime *liveCompetitiveAIRuntime
	aiSenderHuman := false
	if gameID := sender.gameplayGameID(); gameID != 0 {
		aiRuntime = server.competitiveAIRuntimeForGame(gameID)
		if aiRuntime != nil && aiRuntime.roomID == roomID {
			// Snapshot runtime membership before entering roomPeerUDPMu below.
			// The AI world tick holds runtime.mu while resolving native UDP
			// recipients, which takes roomPeerUDPMu. Taking the same locks in
			// reverse order here deadlocks on the first client multicast after
			// virtual movement/bomb projection.
			aiRuntime.mu.Lock()
			_, aiSenderHuman = aiRuntime.humanIDs[packet.Header.PlayerID]
			aiRuntime.mu.Unlock()
		}
	}
	eligiblePeers := make(map[uint32]uint16)
	server.liveMu.RLock()
	candidates := make([]*connectionSession, 0, len(server.liveSessions))
	for _, live := range server.liveSessions {
		uin := live.liveUIN.Load()
		if uin == 0 || uin == packet.Header.UIN || live.liveRoomID.Load() != uint32(roomID) {
			continue
		}
		candidates = append(candidates, live)
	}
	server.liveMu.RUnlock()
	for _, live := range candidates {
		uin, playerID := live.liveUIN.Load(), live.routingPlayerID()
		matchesRoom := live.liveRoomID.Load() == uint32(roomID)
		if uin != 0 && uin != packet.Header.UIN && matchesRoom && snapshotHasRoomPeer(snapshot, playerID) {
			eligiblePeers[uin] = playerID
		}
	}

	server.roomPeerUDPMu.Lock()
	defer server.roomPeerUDPMu.Unlock()
	server.pruneRoomPeerUDPEndpointsLocked(now)
	presence, found := server.roomPeerUDPPresence[packet.Header.UIN]
	if !found || presence.PlayerID != packet.Header.PlayerID || !sameLegacyUDPEndpoint(presence.Address, observedSource) {
		return 0, nil, nil, 0, fmt.Errorf("sender mode-1 endpoint %s has no current Type-1 presence", remote)
	}

	resolved := make([]roomPeerUDPEndpoint, 0, len(packet.Multicast.Targets))
	seen := make(map[roomPeerUDPKey]struct{}, len(packet.Multicast.Targets))
	virtualTargets := 0
	for index, requested := range packet.Multicast.Targets {
		if aiRuntime != nil && aiRuntime.roomID == roomID && aiSenderHuman {
			if _, virtual := aiRuntime.virtualProjection(requested.PlayerID, requested.UIN); virtual {
				matchedKey := roomPeerUDPKey{RoomID: roomID, UIN: requested.UIN}
				if _, duplicate := seen[matchedKey]; duplicate {
					return 0, nil, nil, 0, fmt.Errorf("multicast target %d duplicates UIN %d", index, matchedKey.UIN)
				}
				seen[matchedKey] = struct{}{}
				virtualTargets++
				continue
			}
		}
		playerID, eligible := eligiblePeers[requested.UIN]
		if !eligible || playerID != requested.PlayerID {
			// A stage transition removes dead participants before the surviving
			// clients have consumed NOTIFY_PLAYER_LEAVE. Packets already queued by
			// the original client can therefore still contain the departed target.
			// Ignore that target, but never route to it. Other authenticated current
			// room members in the same datagram remain valid recipients.
			continue
		}
		matchedKey := roomPeerUDPKey{RoomID: roomID, UIN: requested.UIN}
		if _, duplicate := seen[matchedKey]; duplicate {
			return 0, nil, nil, 0, fmt.Errorf("multicast target %d duplicates UIN %d", index, matchedKey.UIN)
		}
		matched, found := server.roomPeerUDPPresence[requested.UIN]
		if !found || matched.PlayerID != requested.PlayerID || matched.Address == nil || matched.Connection == nil {
			continue
		}
		seen[matchedKey] = struct{}{}
		resolved = append(resolved, matched)
	}
	if len(resolved) == 0 && virtualTargets == 0 {
		return 0, nil, nil, 0, fmt.Errorf("multicast contains no current room target with mode-1 presence")
	}
	return roomID, sender, resolved, virtualTargets, nil
}

func (server *Server) validateLegacyUDPSender(header game.LegacyUDPControlHeader, remote *net.UDPAddr) (*connectionSession, error) {
	if remote == nil {
		return nil, fmt.Errorf("observed endpoint is absent")
	}
	remoteIP := remote.IP.String()
	server.liveMu.RLock()
	candidates := make([]*connectionSession, 0, 1)
	for _, live := range server.liveSessions {
		if live.liveUIN.Load() == header.UIN && remoteHost(live.remoteAddress) == remoteIP {
			candidates = append(candidates, live)
		}
	}
	server.liveMu.RUnlock()
	for _, live := range candidates {
		matches := live.liveUIN.Load() == header.UIN && live.routingPlayerID() == header.PlayerID
		if matches {
			return live, nil
		}
	}
	return nil, fmt.Errorf("sender player %d UIN %d has no live session on host %s", header.PlayerID, header.UIN, remoteIP)
}

type roomPeerUDPValidation struct {
	RoomID uint16
}

func (server *Server) validateRoomPeerUDPSender(packet game.LegacyUDPControlPacket, remote *net.UDPAddr) (roomPeerUDPValidation, error) {
	if packet.RoomPeer == nil || remote == nil {
		return roomPeerUDPValidation{}, fmt.Errorf("room-peer packet or observed endpoint is absent")
	}
	remoteIP := remote.IP.String()
	server.liveMu.RLock()
	candidates := make([]*connectionSession, 0, 2)
	for _, live := range server.liveSessions {
		uin := live.liveUIN.Load()
		if uin == packet.Header.UIN && remoteHost(live.remoteAddress) == remoteIP || uin == packet.RoomPeer.TargetUIN {
			candidates = append(candidates, live)
		}
	}
	server.liveMu.RUnlock()
	var sender, target *connectionSession
	for _, live := range candidates {
		uin, playerID := live.liveUIN.Load(), live.routingPlayerID()
		if uin == packet.Header.UIN && playerID == packet.Header.PlayerID && remoteHost(live.remoteAddress) == remoteIP {
			sender = live
		}
		if uin == packet.RoomPeer.TargetUIN && playerID == packet.RoomPeer.TargetPlayerID {
			target = live
		}
	}
	if sender == nil {
		return roomPeerUDPValidation{}, fmt.Errorf("sender player %d UIN %d has no live session on host %s", packet.Header.PlayerID, packet.Header.UIN, remoteIP)
	}
	if target == nil {
		return roomPeerUDPValidation{}, fmt.Errorf("target player %d UIN %d has no live session", packet.RoomPeer.TargetPlayerID, packet.RoomPeer.TargetUIN)
	}
	roomID := uint16(sender.liveRoomID.Load())
	if roomID == 0 || target.liveRoomID.Load() != uint32(roomID) {
		return roomPeerUDPValidation{}, fmt.Errorf("sender and target are not in the same active room")
	}
	state, active := server.worldState().Room(roomID)
	if !active {
		return roomPeerUDPValidation{}, fmt.Errorf("room %d is not active", roomID)
	}
	snapshot := state.Snapshot()
	if !snapshotHasRoomPeer(snapshot, packet.Header.PlayerID) || !snapshotHasRoomPeer(snapshot, packet.RoomPeer.TargetPlayerID) {
		return roomPeerUDPValidation{}, fmt.Errorf("sender or target is absent from room %d membership", roomID)
	}
	return roomPeerUDPValidation{RoomID: roomID}, nil
}

func snapshotHasRoomPeer(snapshot roomstate.Snapshot, playerID uint16) bool {
	for _, member := range snapshot.Members {
		if member.PlayerID == playerID {
			return true
		}
	}
	return false
}

func (server *Server) pruneRoomPeerUDPEndpointsLocked(now time.Time) {
	for key, endpoint := range server.roomPeerUDPEndpoints {
		if now.Sub(endpoint.LastUpdated) > roomPeerUDPMode0EndpointLifetime {
			delete(server.roomPeerUDPEndpoints, key)
		}
	}
	// roomPeerUDPPresence is deliberately not time-pruned. Every consumer also
	// validates the UIN, PlayerID, room membership and current live TCP session;
	// unregisterLiveSession removes it when the account really disconnects.
	// A new Type-1 for the same UIN atomically replaces the old endpoint.
}

func (server *Server) clearRoomPeerUDPIdentity(uin uint32) {
	if uin == 0 {
		return
	}
	server.roomPeerUDPMu.Lock()
	delete(server.roomPeerUDPPresence, uin)
	for key := range server.roomPeerUDPEndpoints {
		if key.UIN == uin {
			delete(server.roomPeerUDPEndpoints, key)
		}
	}
	for key := range server.roomPeerRelayStats {
		if key.SenderUIN == uin {
			delete(server.roomPeerRelayStats, key)
		}
	}
	server.roomPeerUDPMu.Unlock()
}

func cloneUDPAddr(address *net.UDPAddr) *net.UDPAddr {
	if address == nil {
		return nil
	}
	clone := *address
	clone.IP = append(net.IP(nil), address.IP...)
	return &clone
}

func observedLegacyUDPEndpoint(address *net.UDPAddr) (game.LegacyUDPEndpoint, error) {
	var endpoint game.LegacyUDPEndpoint
	if address == nil || address.Port <= 0 || address.Port > 65535 {
		return endpoint, fmt.Errorf("observed UDP endpoint is absent or has invalid port")
	}
	ipv4 := address.IP.To4()
	if ipv4 == nil {
		return endpoint, fmt.Errorf("observed UDP endpoint %s is not IPv4", address)
	}
	copy(endpoint.IPv4[:], ipv4)
	endpoint.Port = uint16(address.Port)
	return endpoint, nil
}

func sameLegacyUDPEndpoint(address *net.UDPAddr, endpoint game.LegacyUDPEndpoint) bool {
	if address == nil || address.Port != int(endpoint.Port) {
		return false
	}
	ipv4 := address.IP.To4()
	return ipv4 != nil && [4]byte(ipv4) == endpoint.IPv4
}
