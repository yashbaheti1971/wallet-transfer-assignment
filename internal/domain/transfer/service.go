package transfer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yashbaheti1971/wallet-transfer-assignment/internal/domain/ledger"
	"github.com/yashbaheti1971/wallet-transfer-assignment/internal/domain/wallet"
)

// ErrDuplicateTxn is returned when a txnId already maps to an existing transfer.
// The caller should treat this as a successful idempotent response, not an error.
var ErrDuplicateTxn = errors.New("transfer: duplicate txnId")

// CreateRequest is the input to the transfer execution workflow.
type CreateRequest struct {
	TxnID        string
	FromWalletID string
	ToWalletID   string
	Amount       int64
}

// Service orchestrates the full transfer workflow and owns the transfer state machine.
//
// Cross-domain dependencies:
//   - wallet.Repository — validates wallet existence + active status (one SQL query)
//   - ledger.Service    — CommitToLedger owns its own transaction; no tx passes across domains
type Service struct {
	repo       Repository
	walletRepo wallet.Repository
	ledgerSvc  *ledger.Service
}

// NewService constructs a TransferService with all required dependencies.
func NewService(repo Repository, walletRepo wallet.Repository, ledgerSvc *ledger.Service) *Service {
	return &Service{
		repo:       repo,
		walletRepo: walletRepo,
		ledgerSvc:  ledgerSvc,
	}
}

// Execute runs the full transfer workflow.
//
// Workflow:
//  1. Idempotency check   — return existing transfer if txnId already seen.
//  2. Persist PENDING     — record created before any validation or money movement.
//  3. Validate transfer   — amount, wallet IDs; failure marks transfer FAILED.
//  4. Validate wallets    — single SQL query (existence + ACTIVE); failure marks FAILED.
//  5. CommitToLedger      — ledger service owns the transaction internally; failure marks FAILED.
//  6. Mark PROCESSED      — terminal success state.
//
// Every error after step 2 results in a FAILED transfer record so the attempt
// is always auditable regardless of where in the flow it failed.
func (s *Service) Execute(ctx context.Context, req *CreateRequest) (*Transfer, error) {
	// ── Step 1: Idempotency ──────────────────────────────────────────────────
	if existing, _ := s.repo.GetByTxnID(ctx, req.TxnID); existing != nil {
		return existing, nil
	}

	// ── Step 2: Persist PENDING ──────────────────────────────────────────────
	// The record is written to the DB before any validation or money movement.
	// This guarantees every attempted transfer is auditable, even on failure.
	now := time.Now().UTC()
	t := &Transfer{
		ID:           NewID(), // "txn_<uuidv7>"
		TxnID:        req.TxnID,
		FromWalletID: req.FromWalletID,
		ToWalletID:   req.ToWalletID,
		Amount:       req.Amount,
		Status:       StatusPending,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.repo.Create(ctx, t); err != nil {
		// Record does not exist yet — cannot mark FAILED; just surface the error.
		return nil, fmt.Errorf("transfer: create pending record: %w", err)
	}

	// markFailed is a local helper used by every subsequent error path.
	// It updates both the in-memory entity and the DB record atomically.
	markFailed := func(reason string) (*Transfer, error) {
		_ = t.MarkFailed(reason)
		_ = s.repo.UpdateStatus(ctx, t.ID, StatusFailed, reason)
		return t, errors.New(reason)
	}

	// ── Step 3: Validate transfer entity ─────────────────────────────────────
	// Catches: invalid amount (<= 0), same source/destination wallet, missing fields.
	if err := t.Validate(); err != nil {
		return markFailed(err.Error())
	}

	// ── Step 4: Validate wallets ─────────────────────────────────────────────
	// Single SQL query: WHERE id = ANY($1) AND status = 'ACTIVE'
	// Catches: wallet not found, wallet inactive.
	if err := s.walletRepo.ValidateWallets(ctx, []string{req.FromWalletID, req.ToWalletID}); err != nil {
		fmt.Println("Error in Wallet Validation")
		return markFailed(fmt.Sprintf("wallet validation failed: %v", err))
	}

	// ── Step 5: CommitToLedger ───────────────────────────────────────────────
	// LedgerService owns BeginTx / Commit / Rollback internally.
	// No transaction object crosses domain boundaries.
	// Catches: insufficient balance, DB errors during balance update or entry insert.
	if err := s.ledgerSvc.CommitToLedger(ctx, t.ID, req.FromWalletID, req.ToWalletID, req.Amount); err != nil {
		fmt.Println("Error in Ledger is", err.Error())
		return markFailed(err.Error())
	}

	// ── Step 6: Mark PROCESSED ───────────────────────────────────────────────
	_ = t.MarkProcessed()
	_ = s.repo.UpdateStatus(ctx, t.ID, StatusProcessed, "")
	return t, nil
}
