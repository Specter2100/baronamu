package main

// 타켓 도달하고 새로 다운로드하면 바로 루트 출력되게
// 데이터베이스 파일 만들기 전에 먼저 노드에 연결이 되는지 확인하고
import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"encoding/json"

	"github.com/utreexo/utreexod/blockchain"
	"github.com/utreexo/utreexod/btcjson"
	"github.com/utreexo/utreexod/btcutil"
	"github.com/utreexo/utreexod/chaincfg"
	"github.com/utreexo/utreexod/chaincfg/chainhash"
	"github.com/utreexo/utreexod/database"
	"github.com/utreexo/utreexod/wire"
)

func main() {
	signet := flag.Bool("signet", false, "Enable Signet network")
	testnet3 := flag.Bool("testnet3", false, "Enable Testnet3 network")
	dataDirFlag := flag.String("datadir", "", "Directory to store data")
	flag.Parse()

	// Data directory setting.
	var dataDir string
	if *dataDirFlag == "" {
		if runtime.GOOS == "windows" {
		} else {
			dataDir = filepath.Join(".", ".utreexod")
		}
	} else {
		dataDir = *dataDirFlag
	}

	// Check network flags.
	if *signet && *testnet3 {
		log.Fatal("Error: --signet and --testnet3 cannot be used together.")
	}

	// Network parameters choice.
	var netParams *chaincfg.Params
	var defaultPort string
	switch {
	case *signet:
		netParams = &chaincfg.SigNetParams
		defaultPort = "38333"
	case *testnet3:
		netParams = &chaincfg.TestNet3Params
		defaultPort = "18333"
	default:
		log.Fatal("Error: Please specify --signet or --testnet3")
	}

	// Data directory creation and database initialization
	dbPath := filepath.Join(dataDir, "Blocks_ffldb")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println("Database not found.")
		db, err := database.Create("ffldb", dbPath, netParams.Net)
		if err != nil {
			log.Fatalf("Failed to create database: %v", err)
		}
		db.Close()
	}

	db, err := database.Open("ffldb", dbPath, netParams.Net)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	utreexo := blockchain.NewUtreexoViewpoint()
	chain, err := blockchain.New(&blockchain.Config{
		DB:          db,
		ChainParams: netParams,
		TimeSource:  blockchain.NewMedianTime(),
		UtreexoView: utreexo,
		Checkpoints: netParams.Checkpoints,
		Interrupt:   nil,
	})
	if err != nil {
		log.Fatalf("Failed to create blockchain: %v", err)
	}
	log.Println("Blockchain initialized successfully!")

	// Connect to DNS seeds for initial peer discovery.
	// The peer is Calvin Kim's UTREEXO seed server.
	//
	// X1 means the archive node.
	// X000001 means the utreexod node.
	dnsSeeds := []string{
		"x1000001.seed.calvinkim.info",
	}
	addrs, err := lookupDNSeeds(dnsSeeds, defaultPort)
	if err != nil {
		log.Fatalf("Failed to lookup DNS seeds: %v", err)
	}

	// DNS 시드에서 가져온 주소들로 연결 시도
	for _, addr := range addrs {
		fmt.Printf("Attempting to connect to node: %s\n", addr)
		conn, err := net.DialTimeout("tcp", addr, 60*time.Second)
		if err != nil {
			log.Printf("Failed to connect to %s: %v", addr, err)
			continue
		}
		err = connectToNode(conn, addr, netParams, chain)
		if err == nil {
			return
		}
		log.Printf("Failed to process connection to %s: %v", addr, err)
	}
	log.Fatal("Failed to connect to any node.")
}

// lookupDNSeeds 특정 DNS 시드에서 IP 주소 조회
func lookupDNSeeds(seeds []string, defaultPort string) ([]string, error) {
	var addrs []string
	for _, seed := range seeds {
		ips, err := net.LookupHost(seed)
		if err != nil {
			log.Printf("Failed to resolve DNS seed %s: %v", seed, err)
			continue
		}
		for _, ip := range ips {
			addrs = append(addrs, fmt.Sprintf("%s:%s", ip, defaultPort))
		}
	}
	return addrs, nil
}

// connectToNode 함수 (Utreexo 지원 확인 및 wtxidrelay 무시 추가)
func connectToNode(conn net.Conn, nodeIP string, netParams *chaincfg.Params, chain *blockchain.BlockChain) error {
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.TCPAddr)
	remoteAddr := conn.RemoteAddr().(*net.TCPAddr)

	verMsg := wire.NewMsgVersion(
		wire.NewNetAddressIPPort(localAddr.IP, uint16(localAddr.Port), wire.SFNodeNetworkLimited|wire.SFNodeWitness),
		wire.NewNetAddressIPPort(remoteAddr.IP, uint16(remoteAddr.Port), 0),
		0,
		0,
	)

	err := wire.WriteMessage(conn, verMsg, wire.FeeFilterVersion, netParams.Net)
	if err != nil {
		return fmt.Errorf("failed to send version message: %v", err)
	}

	for {
		msg, _, err := wire.ReadMessage(conn, wire.FeeFilterVersion, netParams.Net)
		if err != nil {
			if err.Error() == "ReadMessage: unhandled command [wtxidrelay]" {
				fmt.Printf("Received wtxidrelay from %s, ignoring...\n", nodeIP)
				continue // wtxidrelay 메시지 무시
			}
			return fmt.Errorf("failed to read message: %v", err)
		}

		switch m := msg.(type) {
		case *wire.MsgVersion:
			// Utreexo 지원 여부 확인 (SFNodeUtreexo는 utreexod에서 정의된 플래그로 가정)
			if m.Services&wire.SFNodeUtreexo == 0 {
				return fmt.Errorf("node %s does not support Utreexo", nodeIP)
			}
			fmt.Printf("Connected to Utreexo-supporting node: %s\n", nodeIP)
		case *wire.MsgVerAck:
			err = wire.WriteMessage(conn, wire.NewMsgVerAck(), wire.FeeFilterVersion, netParams.Net)
			if err != nil {
				return fmt.Errorf("failed to send verack message: %v", err)
			}
			return requestBlocks(conn, netParams, chain)
		default:
			fmt.Printf("Received message from %s: %T\n", nodeIP, m)
		}
	}
}

// Check network connection and request blocks
// after successful handshake.
// requestBlockst sends a getblocks message to the connected peer
func requestBlocks(conn net.Conn, netParams *chaincfg.Params, chain *blockchain.BlockChain) error {
	targetBlockHash, err := chainhash.NewHashFromStr("0000012965e7e5d073e39cd2efc782054109d9bd359a9560f955f68eff227ef5")
	if err != nil {
		return err
	}

	locator, err := chain.LatestBlockLocator()
	if err != nil {
		return err
	}

	err = sendGetBlocks(conn, netParams, locator, targetBlockHash)
	if err != nil {
		return err
	}

	return processMessages(conn, netParams, chain, targetBlockHash)
}

// Put UtreexoViewpoint into blockchain.
// UtreexoViewpoint is a data structure that holds
// the Utreexo state.
// sendGetBlocks message to peer making a getblocks message
// and sending it.
func sendGetBlocks(conn net.Conn, netParams *chaincfg.Params, blockLocator []*chainhash.Hash, targetBlockHash *chainhash.Hash) error {
	getBlocksMsg := &wire.MsgGetBlocks{
		ProtocolVersion:    wire.FeeFilterVersion,
		BlockLocatorHashes: blockLocator,
		HashStop:           *targetBlockHash,
	}
	err := wire.WriteMessage(conn, getBlocksMsg, wire.FeeFilterVersion, netParams.Net)
	if err != nil {
		return fmt.Errorf("failed to send getblocks message: %v", err)
	}
	return nil
}

// Receive the messages and processing loop.
// Call the handler function for each message type.
func processMessages(conn net.Conn, netParams *chaincfg.Params, chain *blockchain.BlockChain, targetBlockHash *chainhash.Hash) error {

	// Track blocks in queue to avoid duplicates.
	blocksInQueue := make(map[chainhash.Hash]struct{})

	for {
		_, msg, bytes, err := wire.ReadMessageWithEncodingN(conn, wire.FeeFilterVersion, netParams.Net, wire.WitnessEncoding)
		if err != nil {
			log.Printf("Failed to read message: %v, %v", err, msg)
			continue
		}

		// Handle different message types.
		switch m := msg.(type) {

		// Handle MsgInv which contains block hashes and send getdata requests.
		case *wire.MsgInv:
			err = handleInvMessage(m, chain, conn, netParams, blocksInQueue)
			if err != nil {
				return err
			}

		// Handle MsgBlock which contains block data and process it.
		case *wire.MsgBlock:
			block, err := btcutil.NewBlockFromBytes(bytes)
			if err != nil {
				return err
			}
			err = handleBlockMessage(block, chain, blocksInQueue, targetBlockHash, conn, netParams)
			if err != nil {
				return err
			}

		// Handle MsgReject which contains rejection messages.
		case *wire.MsgReject:
			return handleRejectMessage(m)

		// Handle MsgPing which is a ping message and send a pong response.
		case *wire.MsgPing:
			pongMsg := wire.NewMsgPong(m.Nonce)
			err = wire.WriteMessage(conn, pongMsg, wire.FeeFilterVersion, netParams.Net)
			if err != nil {
				log.Printf("Failed to send pong: %v", err)
			}

		default:
			fmt.Printf("Other message: %T\n", m)
		}
	}
}

// Handle MsgInv requesting getdata and getblocks when InvList is empty.
func handleInvMessage(m *wire.MsgInv, chain *blockchain.BlockChain, conn net.Conn, netParams *chaincfg.Params, blocksInQueue map[chainhash.Hash]struct{}) error {
	getDataMsg := wire.NewMsgGetData()
	for _, inv := range m.InvList {
		if inv.Type == wire.InvTypeBlock {
			if chain.IsKnownOrphan(&inv.Hash) {
				continue
			}
			inv.Type = wire.InvTypeWitnessUtreexoBlock
			getDataMsg.AddInvVect(inv)
			blocksInQueue[inv.Hash] = struct{}{}
		}
	}

	err := wire.WriteMessage(conn, getDataMsg, wire.FeeFilterVersion, netParams.Net)
	if err != nil {
		return err
	}
	return nil
}

// Handle and validate MsgBlock adding it to the chain, checking
// for target blocks, and manging the synchronization state.
func handleBlockMessage(block *btcutil.Block, chain *blockchain.BlockChain, blocksInQueue map[chainhash.Hash]struct{}, targetBlockHash *chainhash.Hash, conn net.Conn, netParams *chaincfg.Params) error {
	delete(blocksInQueue, *block.Hash())

	isMainChain, _, err := chain.ProcessBlock(block, blockchain.BFNone)
	if err != nil {
		txs := block.Transactions()
		fmt.Println(txs[0].Hash())
		fmt.Printf("Block validation failed for %s: %v\n", block.Hash().String(), err)
		return nil
	}

	if !isMainChain {
		if len(blocksInQueue) == 0 {
			snapshot := chain.BestSnapshot()
			blockLocator := blockchain.BlockLocator([]*chainhash.Hash{&snapshot.Hash})
			getBlocksMsg := &wire.MsgGetBlocks{
				ProtocolVersion:    wire.FeeFilterVersion,
				BlockLocatorHashes: blockLocator,
				HashStop:           *targetBlockHash,
			}
			return wire.WriteMessage(conn, getBlocksMsg, wire.FeeFilterVersion, netParams.Net)
		}
	}

	utreexorootandleave := &btcjson.GetUtreexoRootsResult{}

	if targetBlockHash.IsEqual(block.Hash()) {
		utreexoView := chain.GetUtreexoView()
		roots := utreexoView.GetRoots()
		rootStrings := make([]string, len(roots))
		for i, root := range roots {
			if root != nil {
				rootStrings[i] = root.String()
			} else {
				rootStrings[i] = ""
			}
		}
		utreexorootandleave.Roots = rootStrings
		utreexorootandleave.NumLeaves = utreexoView.NumLeaves()

		// Print JSON of the Utreexo roots and leaves
		result := struct {
			Roots     []string `json:"roots"`
			NumLeaves uint64   `json:"numleaves"`
		}{
			Roots:     utreexorootandleave.Roots,
			NumLeaves: uint64(utreexorootandleave.NumLeaves),
		}
		jsonOutput, _ := json.MarshalIndent(result, " ", "  ")
		fmt.Println("Congratulations!\nThe target block has been reached.")
		fmt.Println(string(jsonOutput))
		conn.Close()
		os.Exit(0)
	}

	if block.Height()%500 == 0 {
		fmt.Println("Block Height:", block.Height(), "Block Hash:", block.Hash().String())
	}

	if len(blocksInQueue) == 0 {
		blockLocator := blockchain.BlockLocator([]*chainhash.Hash{block.Hash()})
		getBlocksMsg := &wire.MsgGetBlocks{
			ProtocolVersion:    wire.FeeFilterVersion,
			BlockLocatorHashes: blockLocator,
			HashStop:           *targetBlockHash,
		}
		err = wire.WriteMessage(conn, getBlocksMsg, wire.FeeFilterVersion, netParams.Net)
		if err != nil {
			return fmt.Errorf("ailed to send next getblocks: %v", err)
		}
	}
	return nil
}

// Handle MsgReject messages by printing the reason and returning an error.
func handleRejectMessage(m *wire.MsgReject) error {
	fmt.Printf("Reject: %s\n", m.Reason)
	return fmt.Errorf("rejected: %s", m.Reason)
}
