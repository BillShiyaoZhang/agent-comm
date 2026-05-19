	// 2. Fallback: Direct TCP/QUIC -> Relay -> MQ
	fmt.Printf("[Agent] Attempting Direct/Relay P2P stream to %s...\n", targetID)
	
	// Create Double Ratchet session
	drSession, err := dr.NewDRSessionInitiator(ctx, a.Session, a.Keys, targetID, recipientPubKey, recipientURN)
	if err != nil {
		return fmt.Errorf("failed to init DR session: %w", err)
	}

	// Try real-time stream direct delivery via Double Ratchet
	err = drSession.SendMessage(ctx, plaintext)
	if err == nil {
		fmt.Println("[Agent] Message delivered directly via DR realtime stream.")
		// Save advanced ratchet state to store
		// a.DRStore.SaveSession(recipientURN, targetID.String(), drSession.GetRatchetState()) 
		return nil
	}

	fmt.Printf("[Agent] Realtime DR stream failed (%v), falling back to offline MQ blind-store...\n", err)

	// 3. Fallback to MQ Store (Offline Envelope blind drop)
	// We fallback to standard envelope if DR offline envelope builder is not exposed yet.
	env, err := a.Session.BuildEnvelope(recipientPubKey, plaintext)
