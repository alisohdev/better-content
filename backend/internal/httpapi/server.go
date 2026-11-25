package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"bettercontent/backend/internal/assets"
	"bettercontent/backend/internal/content"
	"bettercontent/backend/internal/dna"
	"bettercontent/backend/internal/ideas"
	"bettercontent/backend/internal/identity"
	"bettercontent/backend/internal/transport"
	"bettercontent/backend/internal/workspace"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// Server joins domain-owned HTTP operations at the API composition boundary.
type Server struct {
	auth    *identity.Service
	dna     *dna.Service
	ideas   *ideas.Service
	content *content.Service
	queue   *content.QueueService
	assets  *assets.Service
}

func New(auth *identity.Service, contentDNA *dna.Service, ideaService *ideas.Service, contentService *content.Service, queueService *content.QueueService, assetServices ...*assets.Service) *Server {
	server := &Server{auth: auth, dna: contentDNA, ideas: ideaService, content: contentService, queue: queueService}
	if len(assetServices) > 0 {
		server.assets = assetServices[0]
	}
	return server
}

func (s *Server) GetSession(w http.ResponseWriter, r *http.Request) { s.auth.GetSession(w, r) }
func (s *Server) SignIn(w http.ResponseWriter, r *http.Request)     { s.auth.SignIn(w, r) }
func (s *Server) SignOut(w http.ResponseWriter, r *http.Request)    { s.auth.SignOut(w, r) }
func (s *Server) SignUp(w http.ResponseWriter, r *http.Request)     { s.auth.SignUp(w, r) }

func (s *Server) BeginAssetUpload(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId) {
	if s.assets == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
		return
	}
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		var request transport.BeginAssetUploadRequest
		if err := transport.DecodeJSON(r, &request); err != nil {
			transport.HandleDecodeError(w, err)
			return
		}
		result, err := s.assets.BeginUpload(r.Context(), principal, assets.UploadInput{
			WorkspaceID: workspaceID.String(), MediaType: string(request.MediaType), DisplayName: request.DisplayName,
			OriginalFilename: request.OriginalFilename, ExpectedSizeBytes: int64(request.ExpectedUploadSizeBytes),
			BrowserMIMEType: request.BrowserMimeType,
		})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.BeginAssetUploadResult](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusCreated, response)
	}).ServeHTTP(w, r)
}

func (s *Server) CreateExternalURLAsset(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId) {
	if s.assets == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
		return
	}
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		var request transport.CreateExternalURLAssetRequest
		if err := transport.DecodeJSON(r, &request); err != nil {
			transport.HandleDecodeError(w, err)
			return
		}
		result, err := s.assets.CreateExternalURLAsset(r.Context(), principal, assets.ExternalURLInput{
			WorkspaceID: workspaceID.String(), MediaType: string(request.MediaType),
			DisplayName: request.DisplayName, SourceURL: request.SourceUrl,
		})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.CreateExternalURLAssetResult](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusCreated, response)
	}).ServeHTTP(w, r)
}

func (s *Server) RefreshAssetUploadCapability(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, assetID transport.AssetId) {
	if s.assets == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
		return
	}
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		capability, err := s.assets.RefreshUploadCapability(r.Context(), principal, workspaceID.String(), assetID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.AssetUploadCapability](capability)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) FinalizeAssetUpload(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, assetID transport.AssetId) {
	if s.assets == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
		return
	}
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		result, err := s.assets.FinalizeUpload(r.Context(), principal, workspaceID.String(), assetID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.FinalizeAssetUploadResult](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) IssueAssetReadCapability(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, assetID transport.AssetId) {
	if s.assets == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
		return
	}
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		var request transport.AssetReadCapabilityRequest
		if err := transport.DecodeJSON(r, &request); err != nil {
			transport.HandleDecodeError(w, err)
			return
		}
		capability, err := s.assets.IssueReadCapability(r.Context(), principal, assets.AssetReadCapabilityInput{
			WorkspaceID: workspaceID.String(), AssetID: assetID.String(), Operation: string(request.Operation),
		})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.AssetReadCapability](capability)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) GetAssetLibrary(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, params transport.GetAssetLibraryParams) {
	if s.assets == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
		return
	}
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		input := assets.AssetLibraryInput{WorkspaceID: workspaceID.String()}
		if params.Page != nil {
			input.Page = *params.Page
		}
		if params.Search != nil {
			input.Search = *params.Search
		}
		if params.MediaType != nil {
			value := string(*params.MediaType)
			input.MediaType = &value
		}
		if params.MediaTypes != nil {
			for _, value := range *params.MediaTypes {
				input.MediaTypes = append(input.MediaTypes, string(value))
			}
		}
		if params.AssetIds != nil {
			for _, value := range *params.AssetIds {
				input.AssetIDs = append(input.AssetIDs, value.String())
			}
		}
		if params.Status != nil {
			value := string(*params.Status)
			input.Status = &value
		}
		result, err := s.assets.GetAssetLibrary(r.Context(), principal, input)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.AssetLibraryResult](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) RenameAsset(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, assetID transport.AssetId) {
	if s.assets == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
		return
	}
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		var request transport.RenameAssetRequest
		if err := transport.DecodeJSON(r, &request); err != nil {
			transport.HandleDecodeError(w, err)
			return
		}
		result, err := s.assets.RenameAsset(r.Context(), principal, assets.AssetRenameInput{
			WorkspaceID: workspaceID.String(), AssetID: assetID.String(), DisplayName: request.DisplayName,
		})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.AssetLibraryItem](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) DeleteAsset(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, assetID transport.AssetId) {
	if s.assets == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
		return
	}
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		result, err := s.assets.DeleteAsset(r.Context(), principal, assets.DeleteAssetInput{
			WorkspaceID: workspaceID.String(), AssetID: assetID.String(),
		})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.DeleteAssetResult](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusAccepted, response)
	}).ServeHTTP(w, r)
}

func (s *Server) GetCurrentContentDna(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		result, err := s.dna.GetCurrent(r.Context(), principal, workspaceID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response := transport.CurrentContentDna{Status: transport.CurrentContentDnaStatus(result.Status)}
		if result.CurrentVersion != nil {
			version, err := toContractVersion(*result.CurrentVersion)
			if err != nil {
				writeDomainError(w, err)
				return
			}
			response.CurrentVersion = &version
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) ListContentDnaVersions(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		versions, err := s.dna.ListVersions(r.Context(), principal, workspaceID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response := make([]transport.ContentDnaVersion, 0, len(versions))
		for _, version := range versions {
			converted, err := toContractVersion(version)
			if err != nil {
				writeDomainError(w, err)
				return
			}
			response = append(response, converted)
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) GetContentDnaVersion(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, versionID openapi_types.UUID) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		version, err := s.dna.GetVersion(r.Context(), principal, workspaceID.String(), versionID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := toContractVersion(version)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) SaveContentDna(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		var input struct {
			BaseVersionID json.RawMessage `json:"baseVersionId"`
			Payload       json.RawMessage `json:"payload"`
		}
		if err := transport.DecodeJSON(r, &input); err != nil {
			transport.HandleDecodeError(w, err)
			return
		}
		if len(input.BaseVersionID) == 0 || len(input.Payload) == 0 {
			transport.WriteError(w, http.StatusBadRequest, transport.VALIDATIONERROR, nil)
			return
		}
		var baseVersionID *string
		if string(input.BaseVersionID) != "null" {
			var parsed string
			if err := json.Unmarshal(input.BaseVersionID, &parsed); err != nil {
				transport.WriteError(w, http.StatusBadRequest, transport.VALIDATIONERROR, nil)
				return
			}
			baseVersionID = &parsed
		}
		version, err := s.dna.Save(r.Context(), principal, workspaceID.String(), baseVersionID, input.Payload)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := toContractVersion(version)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) GenerateIdeas(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		var input transport.GenerateIdeasRequest
		if err := transport.DecodeJSON(r, &input); err != nil {
			transport.HandleDecodeError(w, err)
			return
		}
		result, err := s.ideas.Generate(r.Context(), principal, ideas.GenerationInput{
			WorkspaceID: workspaceID.String(), BaseContentDNAVersionID: input.BaseContentDnaVersionId.String(),
			RequestedLanguage: string(input.RequestedLanguage), IdempotencyKey: input.IdempotencyKey.String(),
		})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.IdeaGenerationResult](ideas.ToGenerationResultDTO(result))
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) ListIdeaGenerationHistory(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		result, err := s.ideas.ListBatchHistory(r.Context(), principal, workspaceID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.IdeaGenerationHistoryResult](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) GetIdeaGenerationBatch(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, batchID openapi_types.UUID) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		result, err := s.ideas.GetBatchDetail(r.Context(), principal, workspaceID.String(), batchID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.IdeaGenerationBatchDetail](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) RetryIdeaGenerationBatch(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, batchID openapi_types.UUID) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		result, err := s.ideas.RetryBatch(r.Context(), principal, workspaceID.String(), batchID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.IdeaGenerationResult](ideas.ToGenerationResultDTO(result))
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) GetIdeaLibrary(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, params transport.GetIdeaLibraryParams) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		statusFilter := "NEW"
		if params.StatusFilter != nil {
			statusFilter = string(*params.StatusFilter)
		}
		var batchID *string
		if params.GenerationBatchId != nil {
			value := params.GenerationBatchId.String()
			batchID = &value
		}
		result, err := s.ideas.GetIdeaLibrary(r.Context(), principal, workspaceID.String(), statusFilter, batchID)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.IdeaLibrary](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) UpdateIdeaDecision(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, ideaID openapi_types.UUID) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		var input transport.UpdateIdeaDecisionRequest
		if err := transport.DecodeJSON(r, &input); err != nil {
			transport.HandleDecodeError(w, err)
			return
		}
		result, err := s.ideas.UpdateDecision(r.Context(), principal, ideas.DecisionInput{
			WorkspaceID: workspaceID.String(), IdeaID: ideaID.String(), NextState: string(input.NextState), RejectionReason: input.RejectionReason,
		})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.IdeaDecision](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) GetProductionQueue(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		result, err := s.queue.Get(r.Context(), principal, workspaceID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[[]transport.ProductionQueueItem](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) ReorderProductionQueue(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		var input transport.ReorderProductionQueueRequest
		if err := transport.DecodeJSON(r, &input); err != nil {
			transport.HandleDecodeError(w, err)
			return
		}
		ids := make([]string, len(input.OrderedIdeaIds))
		for index, id := range input.OrderedIdeaIds {
			ids[index] = id.String()
		}
		result, err := s.queue.Reorder(r.Context(), principal, workspaceID.String(), ids)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[[]transport.ProductionQueueItem](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) GenerateContent(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		var input transport.GenerateContentRequest
		if err := transport.DecodeJSON(r, &input); err != nil {
			transport.HandleDecodeError(w, err)
			return
		}
		idempotencyKey := input.IdempotencyKey.String()
		instructions := input.Instructions
		result, err := s.content.Generate(r.Context(), principal, content.ContentGenerationInput{
			WorkspaceID: workspaceID.String(), SourceIdeaID: input.SourceIdeaId.String(),
			BaseContentDNAVersionID: input.BaseContentDnaVersionId.String(),
			RequestedLanguage:       string(input.RequestedLanguage), Format: string(input.Format),
			Instructions: instructions, IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.ContentGenerationResult](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) RetryContentGenerationAttempt(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, attemptID transport.AttemptId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		result, err := s.content.Retry(r.Context(), principal, workspaceID.String(), attemptID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.ContentGenerationResult](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) GetContentGenerationAttemptDetail(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, attemptID transport.AttemptId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		result, err := s.content.ContentGenerationAttemptDetail(r.Context(), principal, workspaceID.String(), attemptID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.ContentGenerationAttemptDetail](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) GetContentGenerationAttemptResult(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, attemptID transport.AttemptId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		result, err := s.content.ContentGenerationAttemptResult(r.Context(), principal, workspaceID.String(), attemptID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		transport.WriteJSON(w, http.StatusOK, result)
	}).ServeHTTP(w, r)
}

func (s *Server) GetIdeaContentGenerationHistory(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, ideaID transport.IdeaId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		result, err := s.content.IdeaContentGenerationHistory(r.Context(), principal, workspaceID.String(), ideaID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.IdeaContentGenerationHistory](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) GetIdeaContentUsage(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, ideaID transport.IdeaId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		result, err := s.content.IdeaContentUsage(r.Context(), principal, workspaceID.String(), ideaID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.IdeaContentUsage](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) GetContentByIdea(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, ideaID transport.IdeaId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		result, err := s.content.ContentByIdea(r.Context(), principal, workspaceID.String(), ideaID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.ContentByIdea](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) ListContents(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		result, err := s.content.List(r.Context(), principal, workspaceID.String())
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[[]transport.ContentListItem](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) GetContentDetail(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, contentID transport.ContentId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		result, err := s.content.Detail(r.Context(), principal, content.ContentTarget{WorkspaceID: workspaceID.String(), ContentID: contentID.String()})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.ContentDetail](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) SaveContentDraft(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, contentID transport.ContentId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		var input transport.SaveContentDraftRequest
		if err := transport.DecodeJSON(r, &input); err != nil {
			transport.HandleDecodeError(w, err)
			return
		}
		documentJSON, err := json.Marshal(input.Document)
		if err != nil {
			transport.WriteError(w, http.StatusBadRequest, transport.VALIDATIONERROR, nil)
			return
		}
		result, err := s.content.SaveDraft(r.Context(), principal, content.ContentTarget{WorkspaceID: workspaceID.String(), ContentID: contentID.String()}, content.SaveDraftInput{
			BaseRevision: input.BaseRevision,
			Document:     documentJSON,
		})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.ContentDraft](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) AcceptContent(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, contentID transport.ContentId) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		var input transport.AcceptContentRequest
		if err := transport.DecodeJSON(r, &input); err != nil {
			transport.HandleDecodeError(w, err)
			return
		}
		result, err := s.content.Accept(r.Context(), principal, content.ContentTarget{WorkspaceID: workspaceID.String(), ContentID: contentID.String()}, input.ExpectedDraftRevision)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.ContentAcceptanceResult](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func (s *Server) GetContentTeleprompter(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, contentID transport.ContentId) {
	s.writeContentProductionView(w, r, workspaceID, contentID, false)
}

func (s *Server) GetContentEditGuide(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, contentID transport.ContentId) {
	s.writeContentProductionView(w, r, workspaceID, contentID, true)
}

func (s *Server) writeContentProductionView(w http.ResponseWriter, r *http.Request, workspaceID transport.WorkspaceId, contentID transport.ContentId, editGuide bool) {
	identity.RequireAuthenticated(s.auth, func(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
		target := content.ContentTarget{WorkspaceID: workspaceID.String(), ContentID: contentID.String()}
		var result content.ProductionView
		var err error
		if editGuide {
			result, err = s.content.EditGuide(r.Context(), principal, target)
		} else {
			result, err = s.content.Teleprompter(r.Context(), principal, target)
		}
		if err != nil {
			writeDomainError(w, err)
			return
		}
		response, err := contractValue[transport.ContentProductionView](result)
		if err != nil {
			transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
			return
		}
		transport.WriteJSON(w, http.StatusOK, response)
	}).ServeHTTP(w, r)
}

func contractValue[T any](value any) (T, error) {
	var result T
	encoded, err := json.Marshal(value)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(encoded, &result)
	return result, err
}

func toContractVersion(version dna.Version) (transport.ContentDnaVersion, error) {
	var id openapi_types.UUID
	if err := id.UnmarshalText([]byte(version.ID)); err != nil {
		return transport.ContentDnaVersion{}, err
	}
	encoded, err := json.Marshal(version.Payload)
	if err != nil {
		return transport.ContentDnaVersion{}, err
	}
	var payload transport.ContentDnaPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return transport.ContentDnaVersion{}, err
	}
	return transport.ContentDnaVersion{
		Id:            id,
		VersionNumber: version.VersionNumber,
		Payload:       payload,
		Readiness:     transport.ContentDnaVersionReadiness(version.Readiness),
		CreatedAt:     version.CreatedAt,
		IsCurrent:     version.IsCurrent,
	}, nil
}

func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, dna.ErrInvalidID), errors.Is(err, dna.ErrInvalidPayload):
		transport.WriteError(w, http.StatusBadRequest, transport.VALIDATIONERROR, nil)
	case errors.Is(err, workspace.ErrUnauthenticated):
		transport.WriteError(w, http.StatusUnauthorized, transport.UNAUTHORIZED, nil)
	case errors.Is(err, assets.ErrUnauthenticated):
		transport.WriteError(w, http.StatusUnauthorized, transport.UNAUTHORIZED, nil)
	case errors.Is(err, workspace.ErrForbidden):
		transport.WriteError(w, http.StatusForbidden, transport.FORBIDDEN, nil)
	case errors.Is(err, dna.ErrConflict):
		transport.WriteError(w, http.StatusConflict, transport.CONFLICT, nil)
	case errors.Is(err, assets.ErrUploadInvalid), errors.Is(err, assets.ErrExternalURLInvalid), errors.Is(err, assets.ErrAssetLibraryInvalid), errors.Is(err, assets.ErrAttachmentInvalid), errors.Is(err, ideas.ErrValidation), errors.Is(err, content.ErrQueueValidation), errors.Is(err, content.ErrGenerationValidation), errors.Is(err, content.ErrDraftValidation):
		transport.WriteError(w, http.StatusBadRequest, transport.VALIDATIONERROR, nil)
	case errors.Is(err, assets.ErrUploadConflict), errors.Is(err, assets.ErrAssetConflict), errors.Is(err, assets.ErrAssetInUse), errors.Is(err, ideas.ErrConflict), errors.Is(err, content.ErrQueueConflict), errors.Is(err, content.ErrGenerationConflict), errors.Is(err, content.ErrDraftConflict):
		transport.WriteError(w, http.StatusConflict, transport.CONFLICT, nil)
	case errors.Is(err, assets.ErrUploadLimited), errors.Is(err, ideas.ErrRateLimited), errors.Is(err, content.ErrGenerationRateLimited):
		transport.WriteError(w, http.StatusTooManyRequests, transport.RATELIMITED, nil)
	case errors.Is(err, assets.ErrStorageUnavailable):
		transport.WriteError(w, http.StatusServiceUnavailable, transport.SERVICEUNAVAILABLE, nil)
	case errors.Is(err, ideas.ErrInvalidOutput), errors.Is(err, content.ErrGenerationInvalidOutput):
		transport.WriteError(w, http.StatusBadGateway, transport.ErrorResponseCodeAIOUTPUTINVALID, nil)
	case errors.Is(err, ideas.ErrProvider), errors.Is(err, content.ErrGenerationProvider):
		transport.WriteError(w, http.StatusServiceUnavailable, transport.ErrorResponseCodePROVIDERERROR, nil)
	case errors.Is(err, assets.ErrAssetNotFound), errors.Is(err, ideas.ErrNotFound), errors.Is(err, content.ErrGenerationNotFound), errors.Is(err, content.ErrContentNotFound):
		transport.WriteError(w, http.StatusNotFound, transport.NOTFOUND, nil)
	case errors.Is(err, dna.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		transport.WriteError(w, http.StatusNotFound, transport.NOTFOUND, nil)
	default:
		transport.WriteError(w, http.StatusInternalServerError, transport.INTERNALERROR, nil)
	}
}

var _ transport.ServerInterface = (*Server)(nil)
