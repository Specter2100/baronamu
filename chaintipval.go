package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"

	"github.com/utreexo/utreexod/blockchain"
	"github.com/utreexo/utreexod/btcutil"
	"github.com/utreexo/utreexod/chaincfg"
	"github.com/utreexo/utreexod/chaincfg/chainhash"
	"github.com/utreexo/utreexod/database"
	"github.com/utreexo/utreexod/wire"
)

func main() {
	signet := flag.Bool("signet", false, "Enable Signet network")
	testnet3 := flag.Bool("testnet3", false, "Enable Testnet3 network")
	connect := flag.String("connect", "", "IP")
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
	if *connect == "" {
		log.Fatal("Error: --connect flag is required.\nUsage: --connect <IP address>")
	}

	// Network paramers choice.
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

	// IP&Port parsing.
	host, port, err := net.SplitHostPort(*connect)
	if err != nil {
		host = *connect
		port = defaultPort
	}

	if net.ParseIP(host) == nil {
		log.Fatal("Error: Invalid IP address.")
	}

	fullAddress := fmt.Sprintf("%s:%s", host, port)
	fmt.Printf("Connecting to node: %s\n", fullAddress)

	// Data directory creation.
	dbPath := filepath.Join(dataDir, "Blocks_ffldb")

	// If there is no database, creat a new one.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println("Database not found. Creating new database...")
		db, err := database.Create("ffldb", dbPath, netParams.Net)
		if err != nil {
			log.Fatalf("Failed to create database: %v", err)
		}
		db.Close()
	}

	// Open database.
	db, err := database.Open("ffldb", dbPath, netParams.Net)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Initialize UtreexoViewpoint.
	utreexo := blockchain.NewUtreexoViewpoint()

	// Blockchain reset.
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

	connectToNode(fullAddress, netParams, chain)
}

// connectToNode connects to the specified node
// and performs the handshake.
// It sends a version message and waits for a verack response.
func connectToNode(nodeIP string, netParams *chaincfg.Params, chain *blockchain.BlockChain) {
	conn, err := net.Dial("tcp", nodeIP)
	if err != nil {
		log.Fatalf("Failed to connect to node: %v", err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.TCPAddr)
	remoteAddr := conn.RemoteAddr().(*net.TCPAddr)

	verMsg := wire.NewMsgVersion(
		wire.NewNetAddressIPPort(localAddr.IP, uint16(localAddr.Port), wire.SFNodeNetworkLimited|wire.SFNodeWitness),
		wire.NewNetAddressIPPort(remoteAddr.IP, uint16(remoteAddr.Port), 0),
		0,
		0,
	)

	err = wire.WriteMessage(conn, verMsg, wire.FeeFilterVersion, netParams.Net)
	if err != nil {
		log.Fatalf("Failed to send version message: %v", err)
	}

	for {
		msg, _, err := wire.ReadMessage(conn, wire.FeeFilterVersion, netParams.Net)
		if err != nil {
			log.Fatalf("Failed to read message: %v", err)
		}

		switch m := msg.(type) {
		case *wire.MsgVerAck:
			err = wire.WriteMessage(conn, wire.NewMsgVerAck(), wire.FeeFilterVersion, netParams.Net)
			if err != nil {
				log.Fatalf("Failed to send verack message: %v", err)
			}
			err = requestBlocks(conn, netParams, chain)
			if err != nil {
				log.Fatalf("Failed during block request: %v", err)
			}
			return
		default:
			fmt.Printf("Received other message: %T\n", m)
		}
	}
}

// Check network connection and request blocks
// after successful handshake.
// requestBlockst sends a getblocks message to the connected peer
func requestBlocks(conn net.Conn, netParams *chaincfg.Params, chain *blockchain.BlockChain) error {
	targetBlockHash, err := chainhash.NewHashFromStr("00000315af6c20d73139312c37dd689ecf68be1b055f145901de5a4482c06270")
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
			fmt.Println("Received ping, sending pong")
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
	fmt.Println("Get Data")
	for _, inv := range m.InvList {
		if inv.Type == wire.InvTypeBlock {
			if chain.IsKnownOrphan(&inv.Hash) {
				continue
			}
			// 블록 높이로 500개 단위로 알람 뜨게

			//	fmt.Println("On Block Hash", inv.Hash.String())
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
// 블록높이가 500으로 나눠떨어질때마다 Height를 출력해라
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
		fmt.Printf("Received orphan block: %s, %v\n", block.Hash().String(), err)
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

	if targetBlockHash.IsEqual(block.Hash()) {
		fmt.Println("Target block reached, exiting")
		utreexoView := chain.GetUtreexoView()
		fmt.Println("Utreexo Viewpoint:", utreexoView.ToString())
		conn.Close()
		os.Exit(0)
	}

	if block.Height()%500 == 0 {
		fmt.Println("Block Height:", block.Height(), "Block Hash:", block.Hash().String())
	}
	// fmt.Println("Blocksinqueue", len(blocksInQueue))
	if len(blocksInQueue) == 1 {
		for k, v := range blocksInQueue {
			fmt.Println(k, v)
		}
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
		fmt.Println("Sent additional getblocks request")
	}
	return nil
}

// Handle MsgReject messages by printing the reason and returning an error.
func handleRejectMessage(m *wire.MsgReject) error {
	fmt.Printf("Reject: %s\n", m.Reason)
	return fmt.Errorf("rejected: %s", m.Reason)
}
