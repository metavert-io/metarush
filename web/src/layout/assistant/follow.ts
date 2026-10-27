import { useEffect } from 'react';
import { call } from '../../api/client';
import { byProject, getPanel, newConversation, openConversation, setPageProject } from './store';

/**
 * The panel follows the project in view (D185): a conversation is about one
 * project, so opening another project shows that project's conversation this
 * tab last had open, else its most recent one anyone can see, else a fresh
 * start there. A page outside any project keeps what is open, and so does a
 * conversation opened on purpose (a link, the list) until it has loaded and
 * said which project it is about.
 */
export function useFollowProject(projectId: string | undefined) {
  useEffect(() => {
    setPageProject(projectId ?? '');
    if (!projectId) return;
    const st = getPanel();
    if (st.projectKey === projectId) return;
    if (st.conversationId && st.projectKey === undefined) return; // opened on purpose; its project is known once it loads
    const remembered = byProject()[projectId];
    if (remembered) {
      openConversation(remembered, projectId);
      return;
    }
    let gone = false;
    // Anything opened or started meanwhile (a message sent, a conversation picked) wins.
    const moved = () => gone || getPanel().projectKey === projectId || getPanel().conversationId !== st.conversationId;
    call('assistant.list', { projectId, limit: 1 })
      .then((out) => {
        if (moved()) return;
        const c = out.conversations[0];
        if (c) openConversation(c.id, projectId);
        else newConversation(projectId);
      })
      .catch(() => {
        if (!moved()) newConversation(projectId);
      });
    return () => {
      gone = true;
    };
  }, [projectId]);
}
