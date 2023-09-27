// https://docs.joinmastodon.org/entities/Status
export interface Status {
    id: string;
    uri: string;
    created_at: string;
    account: Account;
    content: string;
}

// https://docs.joinmastodon.org/entities/Account/
export interface Account {
    id: string;
    username: string;
    acct: string;
    url: string;
    display_name: string;
}