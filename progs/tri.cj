int n
int r
n += 5
call tri(n, r)

// Parallel triangular number: T(n) = n + (n-1) + ... + 1
// Uses parallel calls: tri(n-1, r) and add(n, r) run concurrently
// T(5) = 15, T(10) = 55
procedure tri(int n, int r)
    if n <= 1 then
        r += n
    else
        local int l = n-1
        local int m = n
        call tri(l,r), add(m,r)
        delocal int m = n
        delocal int l = n-1
    fi n <= 1

procedure add(int n, int r)
    r += n
