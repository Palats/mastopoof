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

export function newFakeStatus(content?: string): Status {
    const username = "fakeuser" + (100 + Math.floor(Math.random() * 800)).toString();
    const id = Math.floor(Math.random() * Number.MAX_SAFE_INTEGER).toString();
    if (content === undefined) {
        content = "Some status content.";
    }
    return {
        id: id,
        uri: `https://example.com/users/${username}/statuses/${id}`,
        url: `https://example.com/@${username}/${id}`,
        account: {
            id: Math.floor(Math.random() * Number.MAX_SAFE_INTEGER).toString(),
            username: username,
            acct: `${username}@example.com`,
            url: `https://example.com/@${username}`,
            display_name: `The account of user ${username}`,
            note: "Fake user",
            avatar: "http://www.gravatar.com/avatar/?d=mp",
            avatar_static: "http://www.gravatar.com/avatar/?d=mp",
        },
        created_at: new Date().toISOString(),
        content: content,
        visibility: "public",
        sensitive: false,
        spoiler_text: "",
        media_attachments: [],
        mentions: [],
        tags: [],
        emojis: [],
        reblogs_count: 0,
        favourites_count: 0,
        replies_count: 0,
        in_reply_to_id: null,
        in_reply_to_account_id: null,
        reblog: null,
        poll: null,
        card: null,
        language: null,
        text: null,
        edited_at: null,
    }
}