// The dashboard: lists the jobs and how far each one is.
export interface JobRow {
  path: string;
  done: boolean;
}

export async function loadJobs(): Promise<JobRow[]> {
  const res = await fetch('/api/jobs');
  if (!res.ok) throw new Error(`the server said ${res.status}`);
  return (await res.json()) as JobRow[];
}

export function summary(rows: JobRow[]): string {
  const done = rows.filter((r) => r.done).length;
  return `${done} of ${rows.length} clips done`;
}
