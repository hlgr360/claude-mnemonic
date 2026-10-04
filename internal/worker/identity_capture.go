package worker

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/lukaszraczylo/claude-mnemonic/internal/projects"
)

// identityRecheck is how long a project/folder pair is trusted before git is asked about it again.
const identityRecheck = time.Hour

// noteProjectPath records, in the background, where the project's folder came from (its git remote and checkout root)
// when the project was used from path. It is called wherever a project id and a folder arrive together (session
// context, prompt search, resolving a path), costs one short git call per project and folder per hour, never blocks
// the request, and never fails it. Nothing is sent anywhere, and credentials in a remote are stripped before storing.
func (s *Service) noteProjectPath(project, path string) {
	project = strings.TrimSpace(project)
	path = strings.TrimPrefix(strings.TrimSpace(path), "file://")
	if s == nil || s.identityStore == nil || project == "" || !filepath.IsAbs(path) || path == "/" {
		return
	}
	if ValidateProjectName(project) != nil {
		return
	}
	key := project + "\x00" + path
	if at, ok := s.identitySeen.Load(key); ok && time.Since(at.(time.Time)) < identityRecheck {
		return
	}
	select {
	case s.identitySlots <- struct{}{}:
	default:
		return // busy: it will be noticed the next time this project is used
	}
	s.identitySeen.Store(key, time.Now())

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.identitySlots }()
		ctx, cancel := context.WithTimeout(s.ctxOrBackground(), 6*time.Second)
		defer cancel()
		id := projects.GitIdentity(ctx, path)
		if id.Root == "" && id.Remote == "" {
			return
		}
		if err := s.identityStore.RecordIdentity(ctx, project, id.Remote, id.Root); err != nil {
			log.Debug().Err(err).Str("project", project).Msg("Could not record the project's identity")
			s.identitySeen.Delete(key)
		}
	}()
}

func (s *Service) ctxOrBackground() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}
