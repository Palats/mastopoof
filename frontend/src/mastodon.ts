// https://docs.joinmastodon.org/entities/Status
export interface Status {
    id: string;
    uri: string;
    created_at: string;
    account: Account;
    content: string;
    visibility: "public" | "unlisted" | "private" | "direct";
    sensitive: boolean;
    spoiler_text: string;
    media_attachments: MediaAttachment[];
    // application?: Hash
    mentions: StatusMention[];
    tags: StatusTag[];
    emojis: CustomEmoji[];
    reblogs_count: Number;
    favourites_count: Number;
    replies_count: Number;
    url: string | null;
    in_reply_to_id: string | null;
    in_reply_to_account_id: string | null;
    reblog: Status | null;
    poll: Poll | null;
    card: PreviewCard | null;
    language: string | null;
    text: string | null;
    edited_at: string | null;
    favourited?: boolean;
    reblogged?: boolean;
    muted?: boolean;
    bookmarked?: boolean;
    pinned?: boolean;
    filtered?: FilterResult[];
}

// https://docs.joinmastodon.org/entities/Status/#Mention
export interface StatusMention { }

// https://docs.joinmastodon.org/entities/Status/#Tag
export interface StatusTag {
    name: string;
    url: string;
}

// https://docs.joinmastodon.org/entities/Account/
export interface Account {
    id: string;
    username: string;
    acct: string;
    url: string;
    display_name: string;
    note: string;
    avatar: string;
    avatar_static: string;
}

// https://docs.joinmastodon.org/entities/MediaAttachment/
export interface MediaAttachment {
    id: string;
    type: "unknown" | "image" | "gifv" | "video" | "audio";
    url: string;
    preview_url: string;
    remote_url: string | null;
    // meta: Hash;
    description: string;
    blurhash: string;
}

// https://docs.joinmastodon.org/entities/CustomEmoji/
export interface CustomEmoji { }

// https://docs.joinmastodon.org/entities/Poll/
export interface Poll { }

// https://docs.joinmastodon.org/entities/PreviewCard/
export interface PreviewCard { }

// https://docs.joinmastodon.org/entities/FilterResult/
export interface FilterResult { }