export function getServerSession(): Promise<{ user: { id: string } } | null> {
  throw new Error("The spike must inject a request-scoped Better Auth session.");
}
